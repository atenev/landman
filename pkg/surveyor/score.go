// Package surveyor implements the convergence scoring and diff logic used by
// the Gas Town Surveyor reconciler agent (ADR-0002, ADR-0009).
//
// This package provides pure functions with no Dolt dependency so that the
// scoring logic is unit-testable in isolation from the Dolt connection.
package surveyor

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Resource weights as defined in ADR-0009 Decision 3.
const (
	WeightRig           = 3
	WeightPolecatPool   = 2
	WeightCustomRoleRig = 2
	WeightCustomRoleTown = 3
	WeightFormula       = 1
)

// VerifyConfig holds the convergence verification parameters from the
// Surveyor [verify] configuration block (ADR-0009 Decision 4).
type VerifyConfig struct {
	// ConvergenceThreshold is the minimum score required to merge the reconcile
	// branch. Must be in (0.0, 1.0]. Defaults to 1.0 (production profile).
	ConvergenceThreshold float64
	// StaleTTL is the duration after which a last_seen timestamp is considered
	// stale (process unhealthy). Default: 60s (2× Deacon heartbeat).
	StaleTTL time.Duration
	// BaseDelay is the initial verify retry delay. Default: 5s.
	BaseDelay time.Duration
	// MaxDelay caps the retry delay. Default: 120s.
	MaxDelay time.Duration
	// MaxRetries is the maximum number of verify attempts before escalation.
	// Default: 10.
	MaxRetries int
}

// DefaultProductionConfig returns VerifyConfig with production-profile defaults
// (ADR-0009 Decision 4).
func DefaultProductionConfig() VerifyConfig {
	return VerifyConfig{
		ConvergenceThreshold: 1.0,
		StaleTTL:             60 * time.Second,
		BaseDelay:            5 * time.Second,
		MaxDelay:             120 * time.Second,
		MaxRetries:           10,
	}
}

// DefaultDevelopmentConfig returns VerifyConfig with development-profile
// defaults (ADR-0009 Decision 4).
func DefaultDevelopmentConfig() VerifyConfig {
	return VerifyConfig{
		ConvergenceThreshold: 0.9,
		StaleTTL:             60 * time.Second,
		BaseDelay:            5 * time.Second,
		MaxDelay:             120 * time.Second,
		MaxRetries:           10,
	}
}

// RetryDelay computes the exponential backoff delay for verify attempt n
// (0-indexed). Delay is capped at cfg.MaxDelay (ADR-0009 Decision 5).
// No jitter is applied here; the caller may add ±10% jitter if needed.
func RetryDelay(cfg VerifyConfig, n int) time.Duration {
	d := float64(cfg.BaseDelay) * math.Pow(2, float64(n))
	if d > float64(cfg.MaxDelay) {
		d = float64(cfg.MaxDelay)
	}
	return time.Duration(d)
}

// ─── Topology state snapshots ────────────────────────────────────────────────

// RigState mirrors the actual_rigs table row used by the Surveyor verify loop.
type RigState struct {
	Name     string
	Enabled  bool
	Status   string    // "starting" | "running" | "draining" | "stopped" | "failed"
	LastSeen time.Time
}

// AgentState mirrors one row from actual_agent_config.
type AgentState struct {
	RigName  string
	Role     string
	Status   string    // "starting" | "running" | "stopped" | "failed" | "crashed"
	LastSeen time.Time
}

// WorktreeState mirrors one row from actual_worktrees for Polecat pool scoring.
type WorktreeState struct {
	RigName  string
	Status   string    // "active" | "idle" | "stale"
	LastSeen time.Time
}

// CustomRoleState mirrors one row from actual_custom_roles.
type CustomRoleState struct {
	RigName       string // "__town__" for town-scoped roles
	RoleName      string
	InstanceIndex int
	Status        string
	LastSeen      time.Time
}

// DesiredRig is a minimal desired-state record derived from desired_rigs +
// desired_agent_config for scoring purposes.
type DesiredRig struct {
	Name        string
	Enabled     bool
	MaxPolecats int
	// WitnessEnabled indicates whether a Witness agent is desired.
	WitnessEnabled bool
}

// DesiredCustomRole describes one desired custom role instance for scoring.
type DesiredCustomRole struct {
	Name          string
	Scope         string // "rig" | "town"
	RigName       string // only meaningful when Scope="rig"
	InstanceIndex int
}

// ActualTopology is the full snapshot of actual_topology tables read from
// Dolt `main` during the verify loop.
type ActualTopology struct {
	Rigs        []RigState
	Agents      []AgentState
	Worktrees   []WorktreeState
	CustomRoles []CustomRoleState
}

// ─── Convergence scoring ────────────────────────────────────────────────────

// ScoreResult holds the convergence score and per-category sub-scores.
type ScoreResult struct {
	// Score is the weighted convergence fraction in [0.0, 1.0].
	Score float64
	// RigPass / RigTotal are the rig-level convergence counts.
	RigPass, RigTotal int
	// PoolPass / PoolTotal are the Polecat pool convergence counts.
	PoolPass, PoolTotal int
	// RigRolePass / RigRoleTotal are rig-scoped custom role convergence counts.
	RigRolePass, RigRoleTotal int
	// TownRolePass / TownRoleTotal are town-scoped custom role convergence counts.
	TownRolePass, TownRoleTotal int
	// FormulaPass / FormulaTotal are formula convergence counts.
	FormulaPass, FormulaTotal int
	// NonConverged lists human-readable descriptions of non-converged resources.
	NonConverged []string
}

// ComputeScore computes the convergence score for the given desired and actual
// topology snapshots (ADR-0009 Decision 3).
//
// now is the reference time for staleness checks. Pass time.Now() in production
// code; pass a fixed value in tests.
func ComputeScore(
	desired []DesiredRig,
	desiredCustomRoles []DesiredCustomRole,
	actual ActualTopology,
	cfg VerifyConfig,
	now time.Time,
) ScoreResult {
	var res ScoreResult

	// Build actual lookup indexes.
	actualRigByName := make(map[string]RigState, len(actual.Rigs))
	for _, r := range actual.Rigs {
		actualRigByName[r.Name] = r
	}
	actualAgentsByRig := make(map[string][]AgentState)
	for _, a := range actual.Agents {
		actualAgentsByRig[a.RigName] = append(actualAgentsByRig[a.RigName], a)
	}
	activeWorktreesByRig := make(map[string]int)
	staleWorktreesByRig := make(map[string]int)
	for _, wt := range actual.Worktrees {
		if wt.Status == "active" {
			activeWorktreesByRig[wt.RigName]++
		}
		if wt.Status == "stale" {
			staleWorktreesByRig[wt.RigName]++
		}
	}
	actualCustomRoleKey := func(rigName, roleName string, idx int) string {
		return fmt.Sprintf("%s|%s|%d", rigName, roleName, idx)
	}
	actualCustomRoleMap := make(map[string]CustomRoleState, len(actual.CustomRoles))
	for _, cr := range actual.CustomRoles {
		k := actualCustomRoleKey(cr.RigName, cr.RoleName, cr.InstanceIndex)
		actualCustomRoleMap[k] = cr
	}

	// Score each desired rig.
	for _, dr := range desired {
		res.RigTotal++
		rigPass := scoreRig(dr, actualRigByName, actualAgentsByRig, cfg.StaleTTL, now)
		if rigPass {
			res.RigPass++
		} else {
			res.NonConverged = append(res.NonConverged,
				fmt.Sprintf("rig/%s: not converged", dr.Name))
		}

		// Polecat pool score (only for enabled rigs with Polecats desired).
		if dr.Enabled && dr.MaxPolecats > 0 {
			res.PoolTotal++
			poolPass := scorePolecatPool(dr, activeWorktreesByRig, staleWorktreesByRig, actualAgentsByRig, cfg.StaleTTL, now)
			if poolPass {
				res.PoolPass++
			} else {
				res.NonConverged = append(res.NonConverged,
					fmt.Sprintf("pool/%s: not converged", dr.Name))
			}
		}
	}

	// Score each desired custom role.
	for _, dcr := range desiredCustomRoles {
		k := actualCustomRoleKey(rigNameForRole(dcr), dcr.Name, dcr.InstanceIndex)
		actual, exists := actualCustomRoleMap[k]
		if dcr.Scope == "town" {
			res.TownRoleTotal++
			if exists && actual.Status == "running" && !isStale(actual.LastSeen, cfg.StaleTTL, now) {
				res.TownRolePass++
			} else {
				res.NonConverged = append(res.NonConverged,
					fmt.Sprintf("custom-role-town/%s[%d]: not converged", dcr.Name, dcr.InstanceIndex))
			}
		} else {
			res.RigRoleTotal++
			if exists && actual.Status == "running" && !isStale(actual.LastSeen, cfg.StaleTTL, now) {
				res.RigRolePass++
			} else {
				res.NonConverged = append(res.NonConverged,
					fmt.Sprintf("custom-role-rig/%s/%s[%d]: not converged", dcr.RigName, dcr.Name, dcr.InstanceIndex))
			}
		}
	}

	// Compute weighted score.
	totalWeight := res.RigTotal*WeightRig +
		res.PoolTotal*WeightPolecatPool +
		res.RigRoleTotal*WeightCustomRoleRig +
		res.TownRoleTotal*WeightCustomRoleTown +
		res.FormulaTotal*WeightFormula

	if totalWeight == 0 {
		res.Score = 1.0 // no resources to converge → trivially converged
		return res
	}

	passWeight := res.RigPass*WeightRig +
		res.PoolPass*WeightPolecatPool +
		res.RigRolePass*WeightCustomRoleRig +
		res.TownRolePass*WeightCustomRoleTown +
		res.FormulaPass*WeightFormula

	res.Score = float64(passWeight) / float64(totalWeight)
	return res
}

// ─── per-resource scoring helpers ───────────────────────────────────────────

func scoreRig(
	dr DesiredRig,
	actualRigByName map[string]RigState,
	actualAgentsByRig map[string][]AgentState,
	staleTTL time.Duration,
	now time.Time,
) bool {
	ar, exists := actualRigByName[dr.Name]
	if !exists {
		return false
	}

	// Disabled rig: converged when actual.enabled=false and status is stopped/draining.
	if !dr.Enabled {
		return !ar.Enabled && (ar.Status == "stopped" || ar.Status == "draining")
	}

	// Layer 1: Dolt state.
	if ar.Status != "running" || ar.Enabled != dr.Enabled {
		return false
	}
	// Layer 2: Process health.
	if isStale(ar.LastSeen, staleTTL, now) {
		return false
	}
	// Mayor must be running and fresh.
	for _, agent := range actualAgentsByRig[dr.Name] {
		if agent.Role == "mayor" && agent.Status == "running" && !isStale(agent.LastSeen, staleTTL, now) {
			return true
		}
	}
	return false
}

func scorePolecatPool(
	dr DesiredRig,
	activeWorktreesByRig map[string]int,
	staleWorktreesByRig map[string]int,
	actualAgentsByRig map[string][]AgentState,
	staleTTL time.Duration,
	now time.Time,
) bool {
	// Layer 1: active worktree count in [0, max_polecats].
	active := activeWorktreesByRig[dr.Name]
	if active > dr.MaxPolecats {
		return false
	}
	// Layer 2: no stale worktrees.
	if staleWorktreesByRig[dr.Name] > 0 {
		return false
	}
	// Layer 2: Witness must be running and fresh.
	if dr.WitnessEnabled {
		for _, agent := range actualAgentsByRig[dr.Name] {
			if agent.Role == "witness" && agent.Status == "running" && !isStale(agent.LastSeen, staleTTL, now) {
				return true
			}
		}
		return false
	}
	return true
}

func rigNameForRole(dcr DesiredCustomRole) string {
	if dcr.Scope == "town" {
		return "__town__"
	}
	return dcr.RigName
}

func isStale(lastSeen time.Time, ttl time.Duration, now time.Time) bool {
	return now.Sub(lastSeen) > ttl
}

// ─── Verify loop logic ───────────────────────────────────────────────────────

// EscalationReason describes why the Surveyor escalated.
type EscalationReason string

const (
	EscalationVerifyExhausted EscalationReason = "verify-exhausted"
	EscalationScoreRegression EscalationReason = "score-regression"
	EscalationDogFailure      EscalationReason = "dog-failure"
)

// VerifyOutcome is the result of one complete verify loop run.
type VerifyOutcome struct {
	// Converged is true if the score reached the threshold.
	Converged bool
	// FinalScore is the score from the last verify attempt.
	FinalScore float64
	// Attempts is the number of verify iterations run.
	Attempts int
	// EscalationReason is non-empty when Converged=false.
	Escalation EscalationReason
}

// RunVerifyLoop simulates the Surveyor verify loop against a series of score
// observations. scores[i] is the convergence score computed on attempt i.
//
// This function is designed for unit testing: the caller supplies the score
// sequence directly. In production code the Surveyor computes each score by
// calling ComputeScore.
//
// Returns VerifyOutcome and the index of the first score that triggered
// escalation or convergence.
func RunVerifyLoop(scores []float64, cfg VerifyConfig) VerifyOutcome {
	var prevScore *float64

	for i, score := range scores {
		// Regression detection: immediate escalation (ADR-0009 Decision 6).
		if prevScore != nil && score < *prevScore {
			return VerifyOutcome{
				Converged:  false,
				FinalScore: score,
				Attempts:   i + 1,
				Escalation: EscalationScoreRegression,
			}
		}

		// Check convergence.
		if score >= cfg.ConvergenceThreshold {
			return VerifyOutcome{
				Converged:  true,
				FinalScore: score,
				Attempts:   i + 1,
			}
		}

		s := score
		prevScore = &s

		// Exhaustion check.
		if i+1 >= cfg.MaxRetries {
			return VerifyOutcome{
				Converged:  false,
				FinalScore: score,
				Attempts:   i + 1,
				Escalation: EscalationVerifyExhausted,
			}
		}
	}

	// scores slice exhausted without convergence.
	finalScore := 0.0
	if len(scores) > 0 {
		finalScore = scores[len(scores)-1]
	}
	return VerifyOutcome{
		Converged:  false,
		FinalScore: finalScore,
		Attempts:   len(scores),
		Escalation: EscalationVerifyExhausted,
	}
}

// FormatEscalationTitle returns the Bead title format for a Surveyor escalation
// (ADR-0009 Decision 7).
func FormatEscalationTitle(uuid string, score float64, reason EscalationReason) string {
	return fmt.Sprintf("RECONCILE ESCALATION: %s score=%.3f reason=%s",
		uuid, score, reason)
}

// FormatEscalationSummary returns the structured summary section of an
// escalation Bead description (ADR-0009 Decision 7).
func FormatEscalationSummary(
	uuid string,
	score, threshold float64,
	attempts int,
	durationSec int,
	reason EscalationReason,
	result ScoreResult,
) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Reconcile attempt %s failed to converge.\n\n", uuid)
	fmt.Fprintf(&b, "Reason: %s\n\n", reason)
	fmt.Fprintf(&b, "## Summary\n")
	fmt.Fprintf(&b, "- Convergence score: %.3f (threshold: %.3f)\n", score, threshold)
	fmt.Fprintf(&b, "- Retry attempts: %d\n", attempts)
	fmt.Fprintf(&b, "- Total duration: %ds\n", durationSec)
	fmt.Fprintf(&b, "\n## Sub-scores\n")
	fmt.Fprintf(&b, "- Rigs:                %d/%d (weight %d)\n", result.RigPass, result.RigTotal, WeightRig)
	fmt.Fprintf(&b, "- Polecat pools:       %d/%d (weight %d)\n", result.PoolPass, result.PoolTotal, WeightPolecatPool)
	fmt.Fprintf(&b, "- Custom roles (rig):  %d/%d (weight %d)\n", result.RigRolePass, result.RigRoleTotal, WeightCustomRoleRig)
	fmt.Fprintf(&b, "- Custom roles (town): %d/%d (weight %d)\n", result.TownRolePass, result.TownRoleTotal, WeightCustomRoleTown)
	fmt.Fprintf(&b, "- Formulas:            %d/%d (weight %d)\n", result.FormulaPass, result.FormulaTotal, WeightFormula)
	if len(result.NonConverged) > 0 {
		fmt.Fprintf(&b, "\n## Delta (non-converged resources)\n")
		for _, nc := range result.NonConverged {
			fmt.Fprintf(&b, "- %s\n", nc)
		}
	}
	return b.String()
}
