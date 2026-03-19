package townctl_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/townctl"
)

// ── PatrolRow.PctUsed ─────────────────────────────────────────────────────────

func TestPatrolRow_PctUsed_USD(t *testing.T) {
	row := townctl.PatrolRow{
		BudgetType:  "usd",
		DailyBudget: 50.0,
		Spend:       40.0,
	}
	if got := row.PctUsed(); got != 80.0 {
		t.Errorf("PctUsed() = %.1f, want 80.0", got)
	}
}

func TestPatrolRow_PctUsed_Messages(t *testing.T) {
	row := townctl.PatrolRow{
		BudgetType:  "messages",
		DailyBudget: 500,
		Spend:       450,
	}
	if got := row.PctUsed(); got != 90.0 {
		t.Errorf("PctUsed() = %.1f, want 90.0", got)
	}
}

func TestPatrolRow_PctUsed_NullSpend(t *testing.T) {
	// A rig with no entries in cost_ledger_24h has Spend=0 (NULL normalised).
	row := townctl.PatrolRow{
		BudgetType:  "usd",
		DailyBudget: 100.0,
		Spend:       0,
	}
	if got := row.PctUsed(); got != 0.0 {
		t.Errorf("PctUsed() = %.1f, want 0.0 (no spend yet)", got)
	}
}

func TestPatrolRow_PctUsed_ZeroBudget(t *testing.T) {
	// Guard against divide-by-zero on a malformed row.
	row := townctl.PatrolRow{BudgetType: "usd", DailyBudget: 0, Spend: 10}
	if got := row.PctUsed(); got != 0 {
		t.Errorf("PctUsed() = %.1f with zero budget, want 0", got)
	}
}

// ── ComputePatrolBeads ────────────────────────────────────────────────────────

// Scenario 6 (task dgt-2au): unrestricted rig — no row in desired_cost_policy
// → caller provides no PatrolRow for it → ComputePatrolBeads never sees it.
func TestComputePatrolBeads_UnrestrictedRig_NoBead(t *testing.T) {
	// An unrestricted rig has no entry in desired_cost_policy.
	// The patrol query (LEFT JOIN off desired_cost_policy) never returns it.
	// Therefore ComputePatrolBeads is called with an empty slice.
	beads := townctl.ComputePatrolBeads(nil)
	if len(beads) != 0 {
		t.Errorf("expected 0 beads for unrestricted rig (empty patrol result), got %d: %+v", len(beads), beads)
	}
}

// Scenario 4 (task dgt-2au): rig at warn threshold → Mayor Bead filed.
func TestComputePatrolBeads_WarnThreshold_MayorBead(t *testing.T) {
	rows := []townctl.PatrolRow{
		{
			RigName:     "backend",
			BudgetType:  "usd",
			DailyBudget: 100.0,
			WarnAtPct:   80,
			Spend:       90.0, // 90% — above warn threshold
		},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead (warn), got %d: %+v", len(beads), beads)
	}
	b := beads[0]
	if b.Action != townctl.PatrolWarn {
		t.Errorf("Action = %v, want PatrolWarn", b.Action)
	}
	if b.AssignTo != "mayor" {
		t.Errorf("AssignTo = %q, want %q", b.AssignTo, "mayor")
	}
	if b.Tag != "cost-warning" {
		t.Errorf("Tag = %q, want %q", b.Tag, "cost-warning")
	}
	if b.Priority != 1 {
		t.Errorf("Priority = %d, want 1 (high)", b.Priority)
	}
	if !strings.Contains(b.Title, "COST WARNING") || !strings.Contains(b.Title, "backend") {
		t.Errorf("Title = %q, want 'COST WARNING: rig backend ...'", b.Title)
	}
	if !strings.Contains(b.Description, "backend") || !strings.Contains(b.Description, "90.0%") {
		t.Errorf("Description = %q, should mention rig and pct", b.Description)
	}
}

// Scenario 5 (task dgt-2au): rig at hard cap → drain Bead filed.
func TestComputePatrolBeads_HardCap_DrainBead(t *testing.T) {
	rows := []townctl.PatrolRow{
		{
			RigName:     "backend",
			BudgetType:  "usd",
			DailyBudget: 50.0,
			WarnAtPct:   80,
			Spend:       51.0, // 102% — over the hard cap
		},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead (drain), got %d: %+v", len(beads), beads)
	}
	b := beads[0]
	if b.Action != townctl.PatrolDrain {
		t.Errorf("Action = %v, want PatrolDrain", b.Action)
	}
	if b.Tag != "cost-cap" {
		t.Errorf("Tag = %q, want %q", b.Tag, "cost-cap")
	}
	if b.Priority != 0 {
		t.Errorf("Priority = %d, want 0 (critical)", b.Priority)
	}
	if !strings.Contains(b.Title, "COST CAP") || !strings.Contains(b.Title, "drain rig backend") {
		t.Errorf("Title = %q, want 'COST CAP: drain rig backend'", b.Title)
	}
	// Spec: description must include rig name, spend, budget, tag.
	for _, want := range []string{"backend", "cost-cap", "Drain all Polecats"} {
		if !strings.Contains(b.Description, want) {
			t.Errorf("Description = %q missing %q", b.Description, want)
		}
	}
}

func TestComputePatrolBeads_ExactlyAtWarnThreshold_WarnBead(t *testing.T) {
	rows := []townctl.PatrolRow{
		{
			RigName:     "frontend",
			BudgetType:  "messages",
			DailyBudget: 300,
			WarnAtPct:   85,
			Spend:       255, // exactly 85%
		},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 1 || beads[0].Action != townctl.PatrolWarn {
		t.Errorf("expected 1 warn bead at exactly warn_at_pct, got %+v", beads)
	}
}

func TestComputePatrolBeads_BelowWarnThreshold_NoBead(t *testing.T) {
	rows := []townctl.PatrolRow{
		{
			RigName:     "backend",
			BudgetType:  "usd",
			DailyBudget: 100.0,
			WarnAtPct:   80,
			Spend:       79.0, // 79% — below warn threshold
		},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 0 {
		t.Errorf("expected 0 beads below warn threshold, got %d: %+v", len(beads), beads)
	}
}

func TestComputePatrolBeads_ExactlyAt100_DrainBead(t *testing.T) {
	rows := []townctl.PatrolRow{
		{
			RigName:     "api",
			BudgetType:  "tokens",
			DailyBudget: 1_000_000,
			WarnAtPct:   80,
			Spend:       1_000_000, // exactly 100%
		},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 1 || beads[0].Action != townctl.PatrolDrain {
		t.Errorf("expected 1 drain bead at exactly 100%%, got %+v", beads)
	}
}

func TestComputePatrolBeads_MultipleRigs(t *testing.T) {
	rows := []townctl.PatrolRow{
		// under-budget: no bead
		{RigName: "safe", BudgetType: "usd", DailyBudget: 100, WarnAtPct: 80, Spend: 50},
		// at warn: warn bead
		{RigName: "warn-rig", BudgetType: "usd", DailyBudget: 100, WarnAtPct: 80, Spend: 90},
		// over cap: drain bead
		{RigName: "capped", BudgetType: "messages", DailyBudget: 300, WarnAtPct: 80, Spend: 310},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 2 {
		t.Fatalf("expected 2 beads (1 warn + 1 drain), got %d: %+v", len(beads), beads)
	}
	byRig := map[string]townctl.PatrolBead{}
	for _, b := range beads {
		byRig[b.RigName] = b
	}
	if byRig["warn-rig"].Action != townctl.PatrolWarn {
		t.Errorf("warn-rig action = %v, want PatrolWarn", byRig["warn-rig"].Action)
	}
	if byRig["capped"].Action != townctl.PatrolDrain {
		t.Errorf("capped action = %v, want PatrolDrain", byRig["capped"].Action)
	}
	if _, ok := byRig["safe"]; ok {
		t.Errorf("safe rig should produce no bead, but got one")
	}
}

func TestComputePatrolBeads_NoSpendYet_NoBead(t *testing.T) {
	// Rig has a policy row but no entries in cost_ledger yet (NULL → 0 spend).
	rows := []townctl.PatrolRow{
		{RigName: "new-rig", BudgetType: "usd", DailyBudget: 100, WarnAtPct: 80, Spend: 0},
	}
	beads := townctl.ComputePatrolBeads(rows)
	if len(beads) != 0 {
		t.Errorf("expected 0 beads for new rig with 0 spend, got %d: %+v", len(beads), beads)
	}
}

// ── PatrolQuery ───────────────────────────────────────────────────────────────

func TestPatrolQuery_ContainsRequiredClauses(t *testing.T) {
	q := townctl.PatrolQuery()
	for _, want := range []string{
		"desired_cost_policy",
		"cost_ledger_24h",
		"LEFT JOIN",
		"pct_used",
		"COALESCE",
		"budget_type",
		"warn_at_pct",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("PatrolQuery() missing %q:\n%s", want, q)
		}
	}
}

func TestPatrolQuery_ExcludesUnrestrictedRigs(t *testing.T) {
	q := townctl.PatrolQuery()
	// Query must drive off desired_cost_policy (inner side), so rigs without
	// a policy row are never returned — unrestricted rigs are excluded.
	if strings.Contains(q, "RIGHT JOIN") {
		t.Error("PatrolQuery should not use RIGHT JOIN (would include unrestricted rigs)")
	}
	// The driving table must be desired_cost_policy, not cost_ledger_24h.
	fromIdx := strings.Index(q, "FROM")
	leftJoinIdx := strings.Index(q, "LEFT JOIN")
	if fromIdx < 0 || leftJoinIdx < 0 {
		t.Fatal("PatrolQuery missing FROM or LEFT JOIN clause")
	}
	fromClause := q[fromIdx:leftJoinIdx]
	if !strings.Contains(fromClause, "desired_cost_policy") {
		t.Errorf("FROM clause should reference desired_cost_policy first, got: %s", fromClause)
	}
}
