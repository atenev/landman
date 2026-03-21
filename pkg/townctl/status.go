// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements the town-ctl status subcommand (ADR-0012). It provides a
// direct Dolt read-only view of fleet health with no dependency on the observer
// binary or Prometheus.
package townctl

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tenev/dgt/pkg/surveyor"
)

// StatusOptions configures a town-ctl status run.
type StatusOptions struct {
	// DoltDSN is the Dolt MySQL DSN. When empty, GT_DOLT_DSN env is used.
	DoltDSN string
	// Output is "text" (default) or "json".
	Output string
	// Rigs, when non-empty, filters output to the named rigs only.
	Rigs []string
	// NoColor disables ANSI colour codes in text output.
	NoColor bool
}

// RigStatusResult holds the status snapshot for one rig.
type RigStatusResult struct {
	Name              string   `json:"name"`
	Status            string   `json:"status"`
	Score             float64  `json:"score"`
	MayorStaleSeconds int64    `json:"mayor_stale_seconds"`
	PoolDesired       int      `json:"pool_desired"`
	PoolActual        int      `json:"pool_actual"`
	NonConverged      []string `json:"non_converged"`
}

// CustomRoleStatusResult holds the status for one custom-role instance.
type CustomRoleStatusResult struct {
	Rig          string `json:"rig"`
	Role         string `json:"role"`
	Instance     int    `json:"instance"`
	Status       string `json:"status"`
	StaleSeconds int64  `json:"stale_seconds"`
}

// BeadsSummaryResult holds open-Beads counts for one (type, priority) bucket.
type BeadsSummaryResult struct {
	Type           string `json:"type"`
	Priority       int    `json:"priority"`
	Count          int    `json:"count"`
	OldestSeconds  int64  `json:"oldest_seconds"`
	Escalation     bool   `json:"escalation,omitempty"`
	EscalationNote string `json:"escalation_note,omitempty"`
}

// CostResult holds 24-hour budget usage for one rig.
type CostResult struct {
	Rig       string  `json:"rig"`
	SpendUSD  float64 `json:"spend_usd"`
	BudgetUSD float64 `json:"budget_usd"`
	Pct       float64 `json:"pct"`
}

// StatusResult is the structured output of Status().
// It contains all data needed for text or JSON rendering.
type StatusResult struct {
	Version     int                      `json:"version"`
	Town        string                   `json:"town"`
	Score       float64                  `json:"score"`
	ReadAt      time.Time                `json:"read_at"`
	Rigs        []RigStatusResult        `json:"rigs"`
	CustomRoles []CustomRoleStatusResult `json:"custom_roles"`
	OpenBeads   []BeadsSummaryResult     `json:"open_beads"`
	Cost        []CostResult             `json:"cost"`
}

// Status reads fleet health from Dolt and returns a StatusResult.
// dsn is the Dolt MySQL DSN. opts controls filtering and output format.
func Status(dsn string, opts StatusOptions) (*StatusResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := ConnectDSN(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	defer db.Close()

	now := time.Now()

	// Read desired topology.
	desiredRigs, err := readDesiredRigs(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read desired_rigs: %w", err)
	}
	desiredAgentRoles, err := readDesiredAgentRoles(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read desired_agent_config: %w", err)
	}
	desiredCustomRoles, err := readDesiredCustomRoles(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read desired_custom_roles: %w", err)
	}
	desiredFormulas, err := readDesiredFormulas(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read desired_formulas: %w", err)
	}

	// Build surveyor.DesiredTopology (augment rigs with witness info).
	witnessEnabledByRig := make(map[string]bool, len(desiredAgentRoles))
	for rigName, roles := range desiredAgentRoles {
		for _, role := range roles {
			if role == "witness" {
				witnessEnabledByRig[rigName] = true
			}
		}
	}
	var desiredTop surveyor.DesiredTopology
	for _, dr := range desiredRigs {
		desiredTop.Rigs = append(desiredTop.Rigs, surveyor.DesiredRig{
			Name:           dr.name,
			Enabled:        dr.enabled,
			MaxPolecats:    dr.maxPolecats,
			WitnessEnabled: witnessEnabledByRig[dr.name],
		})
	}
	for _, dcr := range desiredCustomRoles {
		desiredTop.CustomRoles = append(desiredTop.CustomRoles, surveyor.DesiredCustomRole{
			Name:          dcr.name,
			Scope:         dcr.scope,
			RigName:       dcr.rigName,
			InstanceIndex: dcr.instanceIndex,
		})
	}
	for _, df := range desiredFormulas {
		desiredTop.Formulas = append(desiredTop.Formulas, surveyor.DesiredFormula{
			RigName: df.rigName,
			Name:    df.name,
		})
	}

	// Read actual topology.
	actualRigs, err := readActualRigs(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read actual_rigs: %w", err)
	}
	actualAgents, err := readActualAgents(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read actual_agent_config: %w", err)
	}
	actualWorktrees, err := readActualWorktrees(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read actual_worktrees: %w", err)
	}
	actualCustomRoles, err := readActualCustomRoles(ctx, db.DB)
	if err != nil {
		return nil, fmt.Errorf("status: read actual_custom_roles: %w", err)
	}

	actualTop := surveyor.ActualTopology{
		Rigs:        actualRigs,
		Agents:      actualAgents,
		Worktrees:   actualWorktrees,
		CustomRoles: actualCustomRoles,
	}

	// Read town name from actual_town.
	townName, err := readTownName(ctx, db.DB)
	if err != nil {
		// Non-fatal: use empty string.
		townName = ""
	}

	// Compute global score.
	cfg := surveyor.DefaultProductionConfig()
	globalResult := surveyor.ComputeScore(desiredTop, actualTop, cfg, now)

	// Build per-rig results.
	rigFilter := make(map[string]bool, len(opts.Rigs))
	for _, r := range opts.Rigs {
		rigFilter[r] = true
	}

	// Build actual lookup indexes for per-rig data.
	actualRigByName := make(map[string]surveyor.RigState, len(actualRigs))
	for _, ar := range actualRigs {
		actualRigByName[ar.Name] = ar
	}
	actualAgentsByRig := make(map[string][]surveyor.AgentState)
	for _, a := range actualAgents {
		actualAgentsByRig[a.RigName] = append(actualAgentsByRig[a.RigName], a)
	}
	activeWorktreesByRig := make(map[string]int)
	for _, wt := range actualWorktrees {
		if wt.Status == "active" {
			activeWorktreesByRig[wt.RigName]++
		}
	}

	var rigResults []RigStatusResult
	for _, dr := range desiredRigs {
		if len(rigFilter) > 0 && !rigFilter[dr.name] {
			continue
		}

		// Per-rig score: run ComputeScore with a single-rig subset.
		rigDesired := surveyor.DesiredTopology{
			Rigs: []surveyor.DesiredRig{{
				Name:           dr.name,
				Enabled:        dr.enabled,
				MaxPolecats:    dr.maxPolecats,
				WitnessEnabled: witnessEnabledByRig[dr.name],
			}},
		}
		// Include formulas for this rig.
		for _, df := range desiredFormulas {
			if df.rigName == dr.name {
				rigDesired.Formulas = append(rigDesired.Formulas, surveyor.DesiredFormula{
					RigName: df.rigName,
					Name:    df.name,
				})
			}
		}
		rigResult := surveyor.ComputeScore(rigDesired, actualTop, cfg, now)

		// Mayor staleness.
		var mayorStale int64
		for _, a := range actualAgentsByRig[dr.name] {
			if a.Role == "mayor" {
				elapsed := int64(now.Sub(a.LastSeen).Seconds())
				if elapsed > int64(cfg.StaleTTL.Seconds()) {
					mayorStale = elapsed
				}
				break
			}
		}

		// Pool actual = active worktrees.
		poolActual := activeWorktreesByRig[dr.name]

		// Rig status from actual.
		rigStatus := "unknown"
		if ar, ok := actualRigByName[dr.name]; ok {
			rigStatus = ar.Status
		}

		rigResults = append(rigResults, RigStatusResult{
			Name:              dr.name,
			Status:            rigStatus,
			Score:             rigResult.Score,
			MayorStaleSeconds: mayorStale,
			PoolDesired:       dr.maxPolecats,
			PoolActual:        poolActual,
			NonConverged:      rigResult.NonConverged,
		})
	}

	// Build custom-role results.
	var customRoleResults []CustomRoleStatusResult
	staleTTLSec := int64(cfg.StaleTTL.Seconds())
	for _, cr := range actualCustomRoles {
		if len(rigFilter) > 0 && !rigFilter[cr.RigName] && cr.RigName != "__town__" {
			continue
		}
		rig := cr.RigName
		if rig == "__town__" {
			rig = townName
		}
		var stale int64
		if elapsed := int64(now.Sub(cr.LastSeen).Seconds()); elapsed > staleTTLSec {
			stale = elapsed
		}
		customRoleResults = append(customRoleResults, CustomRoleStatusResult{
			Rig:          rig,
			Role:         cr.RoleName,
			Instance:     cr.InstanceIndex,
			Status:       cr.Status,
			StaleSeconds: stale,
		})
	}

	// Read open Beads summary.
	beadsSummary, err := readOpenBeads(ctx, db.DB, now)
	if err != nil {
		// Non-fatal: return empty slice.
		beadsSummary = nil
	}

	// Read cost data.
	costResults, err := readCostData(ctx, db.DB)
	if err != nil {
		// Non-fatal: return empty slice.
		costResults = nil
	}

	// Use global score; if rig filter is active, recompute against filtered rigs.
	score := globalResult.Score
	if len(rigFilter) > 0 && len(rigResults) > 0 {
		var filtered float64
		for _, r := range rigResults {
			filtered += r.Score
		}
		score = filtered / float64(len(rigResults))
	}

	return &StatusResult{
		Version:     1,
		Town:        townName,
		Score:       score,
		ReadAt:      now,
		Rigs:        rigResults,
		CustomRoles: customRoleResults,
		OpenBeads:   beadsSummary,
		Cost:        costResults,
	}, nil
}

// ─── internal row types ──────────────────────────────────────────────────────

type desiredRigRow struct {
	name        string
	enabled     bool
	maxPolecats int
}

type desiredCustomRoleRow struct {
	name          string
	scope         string
	rigName       string
	instanceIndex int
}

type desiredFormulaRow struct {
	rigName string
	name    string
}

// ─── Dolt read helpers ───────────────────────────────────────────────────────

func readTownName(ctx context.Context, db *sql.DB) (string, error) {
	row := db.QueryRowContext(ctx, `SELECT name FROM actual_town LIMIT 1`)
	var name string
	if err := row.Scan(&name); err != nil {
		return "", err
	}
	return name, nil
}

func readDesiredRigs(ctx context.Context, db *sql.DB) ([]desiredRigRow, error) {
	const q = `SELECT name, enabled, max_polecats FROM desired_rigs`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []desiredRigRow
	for rows.Next() {
		var r desiredRigRow
		if err := rows.Scan(&r.name, &r.enabled, &r.maxPolecats); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// readDesiredAgentRoles returns a map of rig_name → []role for all rows in
// desired_agent_config.
func readDesiredAgentRoles(ctx context.Context, db *sql.DB) (map[string][]string, error) {
	const q = `SELECT rig_name, role FROM desired_agent_config`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]string)
	for rows.Next() {
		var rigName, role string
		if err := rows.Scan(&rigName, &role); err != nil {
			return nil, err
		}
		out[rigName] = append(out[rigName], role)
	}
	return out, rows.Err()
}

func readDesiredCustomRoles(ctx context.Context, db *sql.DB) ([]desiredCustomRoleRow, error) {
	// desired_custom_roles has name+scope; desired_rig_custom_roles maps rig→role.
	// Join to get per-rig instances. For town-scoped roles, use a synthetic rig_name.
	const q = `
SELECT
  r.name,
  r.scope,
  COALESCE(j.rig_name, '__town__') AS rig_name,
  0 AS instance_index
FROM desired_custom_roles r
LEFT JOIN desired_rig_custom_roles j
  ON r.name = j.role_name AND r.scope = 'rig'
WHERE r.scope = 'town'
   OR j.rig_name IS NOT NULL`

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		// Table may not exist in all environments; return empty slice.
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []desiredCustomRoleRow
	for rows.Next() {
		var r desiredCustomRoleRow
		if err := rows.Scan(&r.name, &r.scope, &r.rigName, &r.instanceIndex); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func readDesiredFormulas(ctx context.Context, db *sql.DB) ([]desiredFormulaRow, error) {
	const q = `SELECT rig_name, formula_name FROM desired_formulas`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []desiredFormulaRow
	for rows.Next() {
		var r desiredFormulaRow
		if err := rows.Scan(&r.rigName, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func readActualRigs(ctx context.Context, db *sql.DB) ([]surveyor.RigState, error) {
	const q = `SELECT name, enabled, status, last_seen FROM actual_rigs`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []surveyor.RigState
	for rows.Next() {
		var r surveyor.RigState
		if err := rows.Scan(&r.Name, &r.Enabled, &r.Status, &r.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func readActualAgents(ctx context.Context, db *sql.DB) ([]surveyor.AgentState, error) {
	const q = `SELECT rig_name, role, status, last_seen FROM actual_agent_config`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []surveyor.AgentState
	for rows.Next() {
		var a surveyor.AgentState
		if err := rows.Scan(&a.RigName, &a.Role, &a.Status, &a.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func readActualWorktrees(ctx context.Context, db *sql.DB) ([]surveyor.WorktreeState, error) {
	const q = `SELECT rig_name, status, last_seen FROM actual_worktrees`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []surveyor.WorktreeState
	for rows.Next() {
		var w surveyor.WorktreeState
		if err := rows.Scan(&w.RigName, &w.Status, &w.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func readActualCustomRoles(ctx context.Context, db *sql.DB) ([]surveyor.CustomRoleState, error) {
	const q = `SELECT rig_name, role_name, instance_index, status, last_seen FROM actual_custom_roles`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()
	var out []surveyor.CustomRoleState
	for rows.Next() {
		var cr surveyor.CustomRoleState
		if err := rows.Scan(&cr.RigName, &cr.RoleName, &cr.InstanceIndex, &cr.Status, &cr.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

func readOpenBeads(ctx context.Context, db *sql.DB, now time.Time) ([]BeadsSummaryResult, error) {
	const q = `
SELECT
  type,
  priority,
  COUNT(*) AS cnt,
  MIN(created_at) AS oldest,
  MAX(CASE WHEN priority = 0 THEN 1 ELSE 0 END) AS has_p0
FROM bd_issues
WHERE status IN ('open', 'in_progress')
GROUP BY type, priority
ORDER BY priority ASC, type ASC`

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()

	var out []BeadsSummaryResult
	for rows.Next() {
		var (
			issueType string
			priority  int
			count     int
			oldest    sql.NullTime
			hasP0     int
		)
		if err := rows.Scan(&issueType, &priority, &count, &oldest, &hasP0); err != nil {
			return nil, err
		}
		var oldestSec int64
		if oldest.Valid {
			oldestSec = int64(now.Sub(oldest.Time).Seconds())
		}
		b := BeadsSummaryResult{
			Type:          issueType,
			Priority:      priority,
			Count:         count,
			OldestSeconds: oldestSec,
		}
		if priority == 0 {
			b.Escalation = true
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func readCostData(ctx context.Context, db *sql.DB) ([]CostResult, error) {
	const q = `
SELECT
  p.rig_name,
  COALESCE(SUM(l.amount_usd), 0) AS spend_usd,
  p.daily_budget AS budget_usd
FROM desired_cost_policy p
LEFT JOIN cost_ledger_24h l ON l.rig_name = p.rig_name
GROUP BY p.rig_name, p.daily_budget`

	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, nil //nolint:nilerr
	}
	defer rows.Close()

	var out []CostResult
	for rows.Next() {
		var c CostResult
		if err := rows.Scan(&c.Rig, &c.SpendUSD, &c.BudgetUSD); err != nil {
			return nil, err
		}
		if c.BudgetUSD > 0 {
			c.Pct = (c.SpendUSD / c.BudgetUSD) * 100
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
