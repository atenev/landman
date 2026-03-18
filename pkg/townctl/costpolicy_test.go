package townctl_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

// mustParse decodes TOML and fatals on error.
func mustParse(t *testing.T, tomlStr string) *manifest.TownManifest {
	t.Helper()
	m, err := manifest.Parse([]byte(tomlStr))
	if err != nil {
		t.Fatalf("manifest.Parse: %v", err)
	}
	return m
}

// ── ResolveCostPolicies ───────────────────────────────────────────────────────

const noPolicy = `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

[[rig]]
name   = "docs"
repo   = "/srv/docs"
branch = "main"
`

func TestResolveCostPolicies_NoCostBlocks(t *testing.T) {
	m := mustParse(t, noPolicy)
	rows := townctl.ResolveCostPolicies(m)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows (all unrestricted), got %d: %+v", len(rows), rows)
	}
}

func TestResolveCostPolicies_DefaultsOnlyInherited(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[defaults.cost]
daily_budget_messages = 500

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

[[rig]]
name   = "docs"
repo   = "/srv/docs"
branch = "main"
`
	m := mustParse(t, toml)
	rows := townctl.ResolveCostPolicies(m)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows (both inherit defaults), got %d", len(rows))
	}
	for _, row := range rows {
		if row.BudgetType != "messages" || row.DailyBudget != 500 || row.WarnAtPct != 80 {
			t.Errorf("row %s = %+v, want {messages 500 80}", row.RigName, row)
		}
	}
}

func TestResolveCostPolicies_RigOverridesDefaults(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[defaults.cost]
daily_budget_usd = 200.0

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.cost]
  daily_budget_usd = 50.0
  warn_at_pct      = 75

[[rig]]
name   = "docs"
repo   = "/srv/docs"
branch = "main"
`
	m := mustParse(t, toml)
	rows := townctl.ResolveCostPolicies(m)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	// backend uses its own policy.
	backend := rows[0]
	if backend.RigName != "backend" || backend.BudgetType != "usd" ||
		backend.DailyBudget != 50.0 || backend.WarnAtPct != 75 {
		t.Errorf("backend row = %+v, want {usd 50.0 75}", backend)
	}
	// docs inherits [defaults.cost].
	docs := rows[1]
	if docs.RigName != "docs" || docs.BudgetType != "usd" ||
		docs.DailyBudget != 200.0 || docs.WarnAtPct != 80 {
		t.Errorf("docs row = %+v, want {usd 200.0 80}", docs)
	}
}

func TestResolveCostPolicies_MixedPolicies(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "restricted"
repo   = "/srv/r"
branch = "main"

  [rig.cost]
  daily_budget_tokens = 1000000

[[rig]]
name   = "unrestricted"
repo   = "/srv/u"
branch = "main"
`
	m := mustParse(t, toml)
	rows := townctl.ResolveCostPolicies(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (only restricted rig), got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.RigName != "restricted" || r.BudgetType != "tokens" || r.DailyBudget != 1_000_000 {
		t.Errorf("row = %+v, want {restricted tokens 1000000}", r)
	}
}

func TestResolveCostPolicies_DefaultWarnAtPct(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [rig.cost]
  daily_budget_usd = 10.0
`
	m := mustParse(t, toml)
	rows := townctl.ResolveCostPolicies(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].WarnAtPct != 80 {
		t.Errorf("WarnAtPct = %d, want 80 (default)", rows[0].WarnAtPct)
	}
}

// ── ApplySQL ──────────────────────────────────────────────────────────────────

func TestApplySQL_FirstStatementIsVersionsUpsert(t *testing.T) {
	m := mustParse(t, noPolicy)
	stmts := townctl.ApplySQL(m)
	if len(stmts) < 1 {
		t.Fatal("expected at least 1 statement")
	}
	if !strings.Contains(stmts[0], "desired_topology_versions") ||
		!strings.Contains(stmts[0], "desired_cost_policy") {
		t.Errorf("first statement must upsert desired_topology_versions for desired_cost_policy, got: %s", stmts[0])
	}
}

func TestApplySQL_DeleteAll_WhenNoPolicies(t *testing.T) {
	m := mustParse(t, noPolicy)
	stmts := townctl.ApplySQL(m)
	last := stmts[len(stmts)-1]
	if last != "DELETE FROM desired_cost_policy;" {
		t.Errorf("expected full delete when no policies, got: %s", last)
	}
}

func TestApplySQL_UpsertRowAndCleanup(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.cost]
  daily_budget_usd = 50.0
`
	m := mustParse(t, toml)
	stmts := townctl.ApplySQL(m)
	// Expected: [versions upsert, row upsert, cleanup delete] = 3
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[1], "backend") || !strings.Contains(stmts[1], "usd") {
		t.Errorf("expected backend upsert statement, got: %s", stmts[1])
	}
	if !strings.Contains(stmts[2], "NOT IN") || !strings.Contains(stmts[2], "backend") {
		t.Errorf("expected cleanup NOT IN 'backend', got: %s", stmts[2])
	}
}

func TestApplySQL_CleanupNotInContainsAllActiveRigs(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "rig-a"
repo   = "/srv/a"
branch = "main"

  [rig.cost]
  daily_budget_usd = 10.0

[[rig]]
name   = "rig-b"
repo   = "/srv/b"
branch = "main"

  [rig.cost]
  daily_budget_messages = 200
`
	m := mustParse(t, toml)
	stmts := townctl.ApplySQL(m)
	// Expected: versions + 2 upserts + cleanup = 4
	if len(stmts) != 4 {
		t.Fatalf("expected 4 statements, got %d", len(stmts))
	}
	cleanup := stmts[3]
	if !strings.Contains(cleanup, "rig-a") || !strings.Contains(cleanup, "rig-b") {
		t.Errorf("cleanup should list both rigs in NOT IN, got: %s", cleanup)
	}
}

// ── DryRunPlan ────────────────────────────────────────────────────────────────

func TestDryRunPlan_AddAndRemove(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.cost]
  daily_budget_usd = 50.0
`
	m := mustParse(t, toml)
	current := []townctl.CostPolicyRow{
		{RigName: "old-rig", BudgetType: "messages", DailyBudget: 100, WarnAtPct: 80},
	}
	plan := townctl.DryRunPlan(m, current)
	if len(plan) != 2 {
		t.Fatalf("expected 2 ops (add backend, remove old-rig), got %d: %+v", len(plan), plan)
	}
	actions := map[string]string{}
	for _, op := range plan {
		actions[op.Row.RigName] = op.Action
	}
	if actions["backend"] != "add" {
		t.Errorf("expected backend action=add, got %q", actions["backend"])
	}
	if actions["old-rig"] != "remove" {
		t.Errorf("expected old-rig action=remove, got %q", actions["old-rig"])
	}
}

func TestDryRunPlan_NoOp_WhenUnchanged(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.cost]
  daily_budget_usd = 50.0
  warn_at_pct      = 80
`
	m := mustParse(t, toml)
	current := []townctl.CostPolicyRow{
		{RigName: "backend", BudgetType: "usd", DailyBudget: 50.0, WarnAtPct: 80},
	}
	plan := townctl.DryRunPlan(m, current)
	if len(plan) != 0 {
		t.Errorf("expected 0 ops (no-op), got %d: %+v", len(plan), plan)
	}
}

func TestDryRunPlan_Update_WhenChanged(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.cost]
  daily_budget_usd = 75.0
`
	m := mustParse(t, toml)
	current := []townctl.CostPolicyRow{
		{RigName: "backend", BudgetType: "usd", DailyBudget: 50.0, WarnAtPct: 80},
	}
	plan := townctl.DryRunPlan(m, current)
	if len(plan) != 1 || plan[0].Action != "update" {
		t.Errorf("expected 1 update op, got %+v", plan)
	}
}

// ── FormatDryRun ──────────────────────────────────────────────────────────────

func TestFormatDryRun_NoChanges(t *testing.T) {
	out := townctl.FormatDryRun(nil)
	if !strings.Contains(out, "no changes") {
		t.Errorf("expected 'no changes' in output, got: %q", out)
	}
}

func TestFormatDryRun_ShowsPrefixes(t *testing.T) {
	plan := []townctl.DiffOp{
		{Action: "add", Row: townctl.CostPolicyRow{RigName: "new-rig", BudgetType: "usd", DailyBudget: 10, WarnAtPct: 80}},
		{Action: "update", Row: townctl.CostPolicyRow{RigName: "old-rig", BudgetType: "tokens", DailyBudget: 1e6, WarnAtPct: 90}},
		{Action: "remove", Row: townctl.CostPolicyRow{RigName: "gone-rig"}},
	}
	out := townctl.FormatDryRun(plan)
	if !strings.Contains(out, "+ desired_cost_policy") {
		t.Errorf("expected '+' prefix for add, got: %q", out)
	}
	if !strings.Contains(out, "~ desired_cost_policy") {
		t.Errorf("expected '~' prefix for update, got: %q", out)
	}
	if !strings.Contains(out, "- desired_cost_policy") {
		t.Errorf("expected '-' prefix for remove, got: %q", out)
	}
}
