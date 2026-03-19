package surveyor_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tenev/dgt/pkg/surveyor"
)

// ─── RetryDelay ──────────────────────────────────────────────────────────────

func TestRetryDelay_ExponentialBackoff(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	// Attempt 0: base_delay * 2^0 = 5s
	// Attempt 1: base_delay * 2^1 = 10s
	// Attempt 2: base_delay * 2^2 = 20s
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	for i, w := range want {
		got := surveyor.RetryDelay(cfg, i)
		if got != w {
			t.Errorf("RetryDelay(cfg, %d) = %v, want %v", i, got, w)
		}
	}
}

func TestRetryDelay_CappedAtMaxDelay(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig() // MaxDelay = 120s
	// Attempt 5: 5 * 2^5 = 160s > 120s → should cap at 120s.
	got := surveyor.RetryDelay(cfg, 5)
	if got != 120*time.Second {
		t.Errorf("RetryDelay capped: got %v, want 120s", got)
	}
}

func TestRetryDelay_BackoffSequenceMatches_ADR0009_Example(t *testing.T) {
	// ADR-0009 Decision 5 example: 5, 10, 20, 40, 80, 120, 120, 120, 120, 120
	cfg := surveyor.DefaultProductionConfig()
	wantSeq := []time.Duration{5, 10, 20, 40, 80, 120, 120, 120, 120, 120}
	for i, w := range wantSeq {
		got := surveyor.RetryDelay(cfg, i)
		if got != w*time.Second {
			t.Errorf("attempt %d: got %v, want %v", i, got, w*time.Second)
		}
	}
}

// ─── ComputeScore — empty desired state ──────────────────────────────────────

func TestComputeScore_NoDesiredResources_ScoreIsOne(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	result := surveyor.ComputeScore(nil, nil, surveyor.ActualTopology{}, cfg, time.Now())
	if result.Score != 1.0 {
		t.Errorf("empty desired: score = %.3f, want 1.0", result.Score)
	}
}

// ─── ComputeScore — rig scoring ──────────────────────────────────────────────

func rigWithState(name string, enabled bool, status string, lastSeenAgo time.Duration) surveyor.RigState {
	return surveyor.RigState{
		Name:     name,
		Enabled:  enabled,
		Status:   status,
		LastSeen: time.Now().Add(-lastSeenAgo),
	}
}

func agentWithState(rigName, role, status string, lastSeenAgo time.Duration) surveyor.AgentState {
	return surveyor.AgentState{
		RigName:  rigName,
		Role:     role,
		Status:   status,
		LastSeen: time.Now().Add(-lastSeenAgo),
	}
}

func TestComputeScore_SingleRig_Converged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "backend", Enabled: true, MaxPolecats: 0}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "backend", Enabled: true, Status: "running",
			LastSeen: now.Add(-10 * time.Second),
		}},
		Agents: []surveyor.AgentState{{
			RigName: "backend", Role: "mayor", Status: "running",
			LastSeen: now.Add(-10 * time.Second),
		}},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 1.0 {
		t.Errorf("converged rig: score = %.3f, want 1.0", result.Score)
	}
	if result.RigPass != 1 || result.RigTotal != 1 {
		t.Errorf("rig counts: pass=%d total=%d, want 1/1", result.RigPass, result.RigTotal)
	}
	if len(result.NonConverged) != 0 {
		t.Errorf("unexpected non-converged: %v", result.NonConverged)
	}
}

func TestComputeScore_RigMissingFromActual_NotConverged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "backend", Enabled: true}}
	result := surveyor.ComputeScore(desired, nil, surveyor.ActualTopology{}, cfg, now)
	if result.Score != 0.0 {
		t.Errorf("absent rig: score = %.3f, want 0.0", result.Score)
	}
	if result.RigPass != 0 || result.RigTotal != 1 {
		t.Errorf("rig counts: pass=%d total=%d, want 0/1", result.RigPass, result.RigTotal)
	}
}

func TestComputeScore_RigStaleLastSeen_NotConverged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig() // StaleTTL = 60s
	desired := []surveyor.DesiredRig{{Name: "backend", Enabled: true}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "backend", Enabled: true, Status: "running",
			LastSeen: now.Add(-90 * time.Second), // stale
		}},
		Agents: []surveyor.AgentState{{
			RigName: "backend", Role: "mayor", Status: "running",
			LastSeen: now.Add(-10 * time.Second),
		}},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 0.0 {
		t.Errorf("stale rig: score = %.3f, want 0.0", result.Score)
	}
}

func TestComputeScore_RigNoMayor_NotConverged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "backend", Enabled: true}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "backend", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		// No Mayor agent.
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 0.0 {
		t.Errorf("no mayor: score = %.3f, want 0.0", result.Score)
	}
}

func TestComputeScore_DisabledRig_ConvergedWhenStopped(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "legacy", Enabled: false}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "legacy", Enabled: false, Status: "stopped",
			LastSeen: now.Add(-5 * time.Second),
		}},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 1.0 {
		t.Errorf("disabled rig stopped: score = %.3f, want 1.0", result.Score)
	}
}

func TestComputeScore_DisabledRig_NotConvergedWhenRunning(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "legacy", Enabled: false}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "legacy", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 0.0 {
		t.Errorf("disabled rig still running: score = %.3f, want 0.0", result.Score)
	}
}

// ─── ComputeScore — Polecat pool scoring ─────────────────────────────────────

func TestComputeScore_PolecatPool_Converged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{
		Name: "backend", Enabled: true, MaxPolecats: 5,
		WitnessEnabled: true,
	}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "backend", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		Agents: []surveyor.AgentState{
			{RigName: "backend", Role: "mayor", Status: "running", LastSeen: now.Add(-5 * time.Second)},
			{RigName: "backend", Role: "witness", Status: "running", LastSeen: now.Add(-5 * time.Second)},
		},
		Worktrees: []surveyor.WorktreeState{
			{RigName: "backend", Status: "active"},
			{RigName: "backend", Status: "active"},
		},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 1.0 {
		t.Errorf("converged pool: score = %.3f, want 1.0", result.Score)
	}
	if result.PoolPass != 1 || result.PoolTotal != 1 {
		t.Errorf("pool counts: pass=%d total=%d, want 1/1", result.PoolPass, result.PoolTotal)
	}
}

func TestComputeScore_PolecatPool_StaleWorktree_NotConverged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{
		Name: "backend", Enabled: true, MaxPolecats: 5, WitnessEnabled: true,
	}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "backend", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		Agents: []surveyor.AgentState{
			{RigName: "backend", Role: "mayor", Status: "running", LastSeen: now.Add(-5 * time.Second)},
			{RigName: "backend", Role: "witness", Status: "running", LastSeen: now.Add(-5 * time.Second)},
		},
		Worktrees: []surveyor.WorktreeState{
			{RigName: "backend", Status: "stale"}, // stale worktree
		},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.PoolPass != 0 {
		t.Errorf("stale worktree: pool should not converge, got pool_pass=%d", result.PoolPass)
	}
}

func TestComputeScore_PolecatPool_ExceedsMax_NotConverged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{
		Name: "backend", Enabled: true, MaxPolecats: 3, WitnessEnabled: false,
	}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "backend", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		Agents: []surveyor.AgentState{
			{RigName: "backend", Role: "mayor", Status: "running", LastSeen: now.Add(-5 * time.Second)},
		},
		Worktrees: []surveyor.WorktreeState{
			{RigName: "backend", Status: "active"},
			{RigName: "backend", Status: "active"},
			{RigName: "backend", Status: "active"},
			{RigName: "backend", Status: "active"}, // 4 > max 3
		},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.PoolPass != 0 {
		t.Errorf("over-capacity pool: pool should not converge, got pool_pass=%d", result.PoolPass)
	}
}

// ─── ComputeScore — custom role scoring ──────────────────────────────────────

func TestComputeScore_TownScopedRole_Converged(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "r", Enabled: true}}
	desiredRoles := []surveyor.DesiredCustomRole{{
		Name: "scaler", Scope: "town", RigName: "__town__", InstanceIndex: 0,
	}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "r", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		Agents: []surveyor.AgentState{{
			RigName: "r", Role: "mayor", Status: "running", LastSeen: now.Add(-5 * time.Second),
		}},
		CustomRoles: []surveyor.CustomRoleState{{
			RigName: "__town__", RoleName: "scaler", InstanceIndex: 0,
			Status: "running", LastSeen: now.Add(-5 * time.Second),
		}},
	}
	result := surveyor.ComputeScore(desired, desiredRoles, actual, cfg, now)
	if result.TownRolePass != 1 || result.TownRoleTotal != 1 {
		t.Errorf("town role: pass=%d total=%d, want 1/1", result.TownRolePass, result.TownRoleTotal)
	}
}

func TestComputeScore_RigScopedRole_NotConverged_WhenAbsent(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "r", Enabled: true}}
	desiredRoles := []surveyor.DesiredCustomRole{{
		Name: "auditor", Scope: "rig", RigName: "r", InstanceIndex: 0,
	}}
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "r", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		Agents: []surveyor.AgentState{{
			RigName: "r", Role: "mayor", Status: "running", LastSeen: now.Add(-5 * time.Second),
		}},
		// No custom role row.
	}
	result := surveyor.ComputeScore(desired, desiredRoles, actual, cfg, now)
	if result.RigRolePass != 0 {
		t.Errorf("absent rig role: should not converge, got rig_role_pass=%d", result.RigRolePass)
	}
}

// ─── ComputeScore — weighted average ─────────────────────────────────────────

func TestComputeScore_WeightedAverage_PartialConvergence(t *testing.T) {
	// 1 rig (weight 3) converged, 1 rig (weight 3) not converged = 3/6 = 0.5
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{
		{Name: "rig-a", Enabled: true},
		{Name: "rig-b", Enabled: true},
	}
	// Only rig-a has an actual row with a running Mayor.
	actual := surveyor.ActualTopology{
		Rigs: []surveyor.RigState{{
			Name: "rig-a", Enabled: true, Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
		Agents: []surveyor.AgentState{{
			RigName: "rig-a", Role: "mayor", Status: "running",
			LastSeen: now.Add(-5 * time.Second),
		}},
	}
	result := surveyor.ComputeScore(desired, nil, actual, cfg, now)
	if result.Score != 0.5 {
		t.Errorf("partial convergence: score = %.3f, want 0.5", result.Score)
	}
	if result.RigPass != 1 || result.RigTotal != 2 {
		t.Errorf("rig counts: pass=%d total=%d, want 1/2", result.RigPass, result.RigTotal)
	}
}

func TestComputeScore_NonConvergedListed(t *testing.T) {
	now := time.Now()
	cfg := surveyor.DefaultProductionConfig()
	desired := []surveyor.DesiredRig{{Name: "backend", Enabled: true}}
	result := surveyor.ComputeScore(desired, nil, surveyor.ActualTopology{}, cfg, now)
	if len(result.NonConverged) == 0 {
		t.Error("expected non-converged list to be non-empty")
	}
	if !strings.Contains(result.NonConverged[0], "backend") {
		t.Errorf("non-converged entry should mention 'backend': %v", result.NonConverged[0])
	}
}

// ─── RunVerifyLoop ────────────────────────────────────────────────────────────

func TestRunVerifyLoop_ConvergesOnFirstAttempt(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	outcome := surveyor.RunVerifyLoop([]float64{1.0}, cfg)
	if !outcome.Converged {
		t.Errorf("expected converged, got %+v", outcome)
	}
	if outcome.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", outcome.Attempts)
	}
}

func TestRunVerifyLoop_ConvergesAfterRetries(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	scores := []float64{0.5, 0.7, 0.9, 1.0}
	outcome := surveyor.RunVerifyLoop(scores, cfg)
	if !outcome.Converged {
		t.Errorf("expected converged, got %+v", outcome)
	}
	if outcome.Attempts != 4 {
		t.Errorf("attempts = %d, want 4", outcome.Attempts)
	}
	if outcome.FinalScore != 1.0 {
		t.Errorf("final score = %.3f, want 1.0", outcome.FinalScore)
	}
}

func TestRunVerifyLoop_ExhaustsRetries(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig() // MaxRetries = 10
	// Score never reaches 1.0.
	scores := make([]float64, 15)
	for i := range scores {
		scores[i] = 0.8
	}
	outcome := surveyor.RunVerifyLoop(scores, cfg)
	if outcome.Converged {
		t.Errorf("expected not converged, got converged")
	}
	if outcome.Escalation != surveyor.EscalationVerifyExhausted {
		t.Errorf("escalation = %q, want %q", outcome.Escalation, surveyor.EscalationVerifyExhausted)
	}
	if outcome.Attempts != cfg.MaxRetries {
		t.Errorf("attempts = %d, want %d (max_retries)", outcome.Attempts, cfg.MaxRetries)
	}
}

func TestRunVerifyLoop_RegressionEscalatesImmediately(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	// Score goes up then drops — regression at attempt 2.
	scores := []float64{0.5, 0.8, 0.6, 0.9}
	outcome := surveyor.RunVerifyLoop(scores, cfg)
	if outcome.Converged {
		t.Errorf("regression should not converge")
	}
	if outcome.Escalation != surveyor.EscalationScoreRegression {
		t.Errorf("escalation = %q, want %q", outcome.Escalation, surveyor.EscalationScoreRegression)
	}
	if outcome.Attempts != 3 {
		t.Errorf("attempts = %d, want 3 (escalate at third attempt)", outcome.Attempts)
	}
	if outcome.FinalScore != 0.6 {
		t.Errorf("final score = %.3f, want 0.6", outcome.FinalScore)
	}
}

func TestRunVerifyLoop_ScorePlateauContinues(t *testing.T) {
	// Plateau (same score) should NOT escalate immediately — it should retry
	// until max_retries (ADR-0009 Decision 6).
	cfg := surveyor.VerifyConfig{
		ConvergenceThreshold: 1.0,
		MaxRetries:           3,
		StaleTTL:             60 * time.Second,
		BaseDelay:            5 * time.Second,
		MaxDelay:             120 * time.Second,
	}
	scores := []float64{0.8, 0.8, 0.8, 0.8}
	outcome := surveyor.RunVerifyLoop(scores, cfg)
	if outcome.Escalation != surveyor.EscalationVerifyExhausted {
		t.Errorf("plateau escalation = %q, want %q", outcome.Escalation, surveyor.EscalationVerifyExhausted)
	}
	if outcome.Attempts != 3 {
		t.Errorf("attempts = %d, want 3 (max_retries)", outcome.Attempts)
	}
}

func TestRunVerifyLoop_DevelopmentThreshold_ConvergesAtPointNine(t *testing.T) {
	cfg := surveyor.DefaultDevelopmentConfig() // convergence_threshold = 0.9
	scores := []float64{0.7, 0.85, 0.9}
	outcome := surveyor.RunVerifyLoop(scores, cfg)
	if !outcome.Converged {
		t.Errorf("dev threshold 0.9: expected converged at 0.9, got %+v", outcome)
	}
}

func TestRunVerifyLoop_EmptyScores_Exhausted(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	outcome := surveyor.RunVerifyLoop(nil, cfg)
	if outcome.Converged {
		t.Errorf("empty scores: should not converge")
	}
	if outcome.Attempts != 0 {
		t.Errorf("empty scores: attempts = %d, want 0", outcome.Attempts)
	}
}

// ─── FormatEscalationTitle ────────────────────────────────────────────────────

func TestFormatEscalationTitle_Format(t *testing.T) {
	title := surveyor.FormatEscalationTitle("abc-123", 0.75, surveyor.EscalationVerifyExhausted)
	want := "RECONCILE ESCALATION: abc-123 score=0.750 reason=verify-exhausted"
	if title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
}

func TestFormatEscalationTitle_Regression(t *testing.T) {
	title := surveyor.FormatEscalationTitle("uuid-x", 0.5, surveyor.EscalationScoreRegression)
	if !strings.Contains(title, "score-regression") {
		t.Errorf("expected 'score-regression' in title, got %q", title)
	}
}

// ─── FormatEscalationSummary ─────────────────────────────────────────────────

func TestFormatEscalationSummary_ContainsRequiredSections(t *testing.T) {
	result := surveyor.ScoreResult{
		Score:        0.6,
		RigPass:      2,
		RigTotal:     3,
		PoolPass:     1,
		PoolTotal:    2,
		NonConverged: []string{"rig/backend: not converged"},
	}
	summary := surveyor.FormatEscalationSummary(
		"test-uuid", 0.6, 1.0, 10, 300,
		surveyor.EscalationVerifyExhausted, result,
	)
	checks := []string{
		"test-uuid",
		"verify-exhausted",
		"Convergence score: 0.600",
		"threshold: 1.000",
		"Retry attempts: 10",
		"Total duration: 300s",
		"Rigs:",
		"Polecat pools:",
		"Custom roles",
		"Formulas:",
		"backend",
	}
	for _, c := range checks {
		if !strings.Contains(summary, c) {
			t.Errorf("summary missing %q", c)
		}
	}
}

// ─── Crash recovery scenario (integration-style unit test) ───────────────────

// TestCrashRecoveryScenario models a Surveyor crash mid-reconcile:
// - First verify loop starts, score rises but Surveyor "crashes" (loop exits early)
// - New Surveyor starts fresh (new RunVerifyLoop call) from current state
// - New Surveyor converges
func TestCrashRecoveryScenario_FreshStartConverges(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()

	// First Surveyor: starts but "crashes" after 2 attempts (score plateau).
	firstAttempts := []float64{0.5, 0.7} // loop exits before convergence
	first := surveyor.RunVerifyLoop(firstAttempts, surveyor.VerifyConfig{
		ConvergenceThreshold: 1.0,
		MaxRetries:           2, // crashes here
		StaleTTL:             60 * time.Second,
		BaseDelay:            5 * time.Second,
		MaxDelay:             120 * time.Second,
	})
	if first.Converged {
		t.Fatal("first surveyor should not converge before crash")
	}

	// Second Surveyor: GUPP re-reads state from scratch, converges cleanly.
	secondAttempts := []float64{0.7, 0.85, 1.0}
	second := surveyor.RunVerifyLoop(secondAttempts, cfg)
	if !second.Converged {
		t.Errorf("second surveyor: expected convergence after crash recovery, got %+v", second)
	}
}

// TestConcurrentGuardScenario verifies that regression detection serves as a
// proxy for detecting split-brain: if a second Surveyor makes conflicting writes
// that reduce the convergence score, the first Surveyor escalates.
func TestConcurrentGuardScenario_ScoreRegressionEscalates(t *testing.T) {
	cfg := surveyor.DefaultProductionConfig()
	// Score rises to 0.8 then drops due to a conflicting write.
	scores := []float64{0.5, 0.8, 0.6}
	outcome := surveyor.RunVerifyLoop(scores, cfg)
	if outcome.Escalation != surveyor.EscalationScoreRegression {
		t.Errorf("concurrent conflict: expected score-regression, got %q", outcome.Escalation)
	}
}
