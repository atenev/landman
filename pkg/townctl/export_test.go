package townctl_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/townctl"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

func mustExport(t *testing.T, state townctl.ExportState, opts townctl.ExportOptions) string {
	t.Helper()
	out, err := townctl.Export(state, opts)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return out
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output does not contain %q\nfull output:\n%s", needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("output unexpectedly contains %q\nfull output:\n%s", needle, haystack)
	}
}

// ─── Export (unknown backend) ─────────────────────────────────────────────────

func TestExport_UnknownBackend(t *testing.T) {
	_, err := townctl.Export(townctl.ExportState{}, townctl.ExportOptions{Backend: "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
	if !strings.Contains(err.Error(), "unknown export backend") {
		t.Errorf("error %q should mention unknown backend", err.Error())
	}
}

// ─── GenerateTOML (BackendLocal) ─────────────────────────────────────────────

func TestExport_Local_EmptyState(t *testing.T) {
	out := mustExport(t, townctl.ExportState{}, townctl.ExportOptions{Backend: townctl.BackendLocal})

	assertContains(t, out, `version = "1"`)
	assertContains(t, out, "[town]")
	assertContains(t, out, "FIXME")
	assertContains(t, out, "[secrets]")
	assertContains(t, out, "[defaults]")
	// no [[rig]] section — state has no rigs
	assertNotContains(t, out, "[[rig]]")
}

func TestExport_Local_SingleRig(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "backend", Repo: "/srv/backend", Branch: "main", Enabled: true},
		},
		AgentConfig: []townctl.DesiredAgentConfigRow{
			{RigName: "backend", Role: "mayor", Enabled: true, Model: "claude-opus-4-6"},
			{RigName: "backend", Role: "witness", Enabled: true},
			{RigName: "backend", Role: "refinery", Enabled: true},
			{RigName: "backend", Role: "deacon", Enabled: true},
			{RigName: "backend", Role: "polecat", Enabled: true, MaxCount: 10, Model: "claude-haiku-4-5-20251001"},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendLocal})

	assertContains(t, out, `name    = "backend"`)
	assertContains(t, out, `repo    = "/srv/backend"`)
	assertContains(t, out, `branch  = "main"`)
	assertContains(t, out, "enabled = true")
	assertContains(t, out, "[rig.agents]")
	assertContains(t, out, "mayor    = true")
	assertContains(t, out, "witness  = true")
	assertContains(t, out, "max_polecats = 10")
}

func TestExport_Local_RigWithFormulas(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "backend", Repo: "/srv/backend", Branch: "main", Enabled: true},
		},
		Formulas: []townctl.DesiredFormulaRow{
			{RigName: "backend", Name: "nightly-regression", Schedule: "0 1 * * *"},
			{RigName: "backend", Name: "weekly-scan", Schedule: "0 4 * * 0"},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendLocal})

	assertContains(t, out, "[[rig.formula]]")
	assertContains(t, out, `name     = "nightly-regression"`)
	assertContains(t, out, `schedule = "0 1 * * *"`)
	assertContains(t, out, `name     = "weekly-scan"`)
}

func TestExport_Local_RigWithCustomRoles(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "backend", Repo: "/srv/backend", Branch: "main", Enabled: true},
		},
		CustomRoles: []townctl.DesiredCustomRoleRow{
			{
				Name:         "scaling-agent",
				Scope:        "rig",
				Lifespan:     "persistent",
				TriggerType:  "schedule",
				TriggerSchedule: "*/5 * * * *",
				ParentRole:   "deacon",
				ReportsTo:    "mayor",
				MaxInstances: 1,
				ClaudeMDPath: "${GT_HOME}/roles/scaling-agent/CLAUDE.md",
				Model:        "claude-haiku-4-5-20251001",
			},
		},
		RigCustomRoles: []townctl.DesiredRigCustomRoleRow{
			{RigName: "backend", RoleName: "scaling-agent", Enabled: true},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendLocal})

	assertContains(t, out, "[[role]]")
	assertContains(t, out, `name  = "scaling-agent"`)
	assertContains(t, out, `scope    = "rig"`)
	assertContains(t, out, "[role.identity]")
	assertContains(t, out, `schedule = "*/5 * * * *"`)
	assertContains(t, out, `parent = "deacon"`)
	assertContains(t, out, `reports_to = "mayor"`)
	assertContains(t, out, "max_instances = 1")
	assertContains(t, out, `roles = ["scaling-agent"]`)
}

func TestExport_Local_PerRigCostPolicy(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "backend", Repo: "/srv/backend", Branch: "main", Enabled: true},
			{Name: "frontend", Repo: "/srv/frontend", Branch: "main", Enabled: true},
		},
		CostPolicies: []townctl.CostPolicyRow{
			{RigName: "backend", BudgetType: "usd", DailyBudget: 150.0, WarnAtPct: 80},
			{RigName: "frontend", BudgetType: "usd", DailyBudget: 50.0, WarnAtPct: 75},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendLocal})

	// Different policies — must appear as per-rig, not in [defaults].
	assertContains(t, out, "[rig.cost]")
	assertContains(t, out, "daily_budget_usd = 150.0000")
	assertContains(t, out, "daily_budget_usd = 50.0000")
}

func TestExport_Local_SharedCostPolicyHoistedToDefaults(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "backend", Repo: "/srv/backend", Branch: "main", Enabled: true},
			{Name: "frontend", Repo: "/srv/frontend", Branch: "main", Enabled: true},
		},
		CostPolicies: []townctl.CostPolicyRow{
			{RigName: "backend", BudgetType: "usd", DailyBudget: 100.0, WarnAtPct: 80},
			{RigName: "frontend", BudgetType: "usd", DailyBudget: 100.0, WarnAtPct: 80},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendLocal})

	// Shared policy → hoisted to [defaults.cost], not repeated per rig.
	assertContains(t, out, "[defaults.cost]")
	assertNotContains(t, out, "[rig.cost]")
}

func TestExport_Local_DefaultsModelInference(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "r1", Repo: "/r1", Branch: "main", Enabled: true},
		},
		AgentConfig: []townctl.DesiredAgentConfigRow{
			{RigName: "r1", Role: "mayor", Enabled: true, Model: "claude-opus-4-6"},
			{RigName: "r1", Role: "polecat", Enabled: true, MaxCount: 5, Model: "claude-sonnet-4-6"},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendLocal})

	assertContains(t, out, `mayor_model   = "claude-opus-4-6"`)
	assertContains(t, out, `polecat_model = "claude-sonnet-4-6"`)
	assertContains(t, out, "max_polecats  = 5")
}

func TestExport_Local_ExportQuerySQL(t *testing.T) {
	stmts := townctl.ExportQuerySQL()
	if len(stmts) != 6 {
		t.Fatalf("expected 6 query statements, got %d", len(stmts))
	}
	tables := []string{
		"desired_rigs",
		"desired_agent_config",
		"desired_formulas",
		"desired_custom_roles",
		"desired_rig_custom_roles",
		"desired_cost_policy",
	}
	for i, tbl := range tables {
		if !strings.Contains(stmts[i], tbl) {
			t.Errorf("stmts[%d] does not mention %q: %s", i, tbl, stmts[i])
		}
	}
}

// ─── GenerateCRDs (BackendK8s) ───────────────────────────────────────────────

func TestExport_K8s_GasTownCR(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "backend", Repo: "/srv/backend", Branch: "main", Enabled: true},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{
		Backend:     townctl.BackendK8s,
		Namespace:   "my-ns",
		GasTownName: "my-town",
	})

	assertContains(t, out, "apiVersion: gastown.tenev.io/v1alpha1")
	assertContains(t, out, "kind: GasTown")
	assertContains(t, out, "name: my-town")
	assertContains(t, out, "kind: Rig")
	assertContains(t, out, "name: backend")
	assertContains(t, out, "namespace: my-ns")
}

func TestExport_K8s_DefaultNamespace(t *testing.T) {
	out := mustExport(t, townctl.ExportState{}, townctl.ExportOptions{Backend: townctl.BackendK8s})

	assertContains(t, out, "kind: GasTown")
	assertContains(t, out, "name: gas-town")
}

func TestExport_K8s_AgentRoleCR(t *testing.T) {
	state := townctl.ExportState{
		CustomRoles: []townctl.DesiredCustomRoleRow{
			{
				Name:         "scaling-agent",
				Description:  "Reactive Polecat pool scaler",
				Scope:        "rig",
				Lifespan:     "persistent",
				TriggerType:  "schedule",
				TriggerSchedule: "*/5 * * * *",
				ParentRole:   "deacon",
				ReportsTo:    "mayor",
				MaxInstances: 1,
				ClaudeMDPath: "${GT_HOME}/roles/scaling-agent/CLAUDE.md",
				Model:        "claude-haiku-4-5-20251001",
			},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendK8s})

	assertContains(t, out, "kind: AgentRole")
	assertContains(t, out, "name: scaling-agent")
	assertContains(t, out, `scope: "rig"`)
	assertContains(t, out, `parent: "deacon"`)
	assertContains(t, out, "maxInstances: 1")
	assertContains(t, out, `claudeMD: "${GT_HOME}/roles/scaling-agent/CLAUDE.md"`)
}

func TestExport_K8s_SharedCostInGasTown(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "r1", Repo: "/r1", Branch: "main", Enabled: true},
			{Name: "r2", Repo: "/r2", Branch: "main", Enabled: true},
		},
		CostPolicies: []townctl.CostPolicyRow{
			{RigName: "r1", BudgetType: "usd", DailyBudget: 200.0, WarnAtPct: 75},
			{RigName: "r2", BudgetType: "usd", DailyBudget: 200.0, WarnAtPct: 75},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendK8s})

	// Shared cost → emitted in GasTown CR, not in each Rig CR.
	assertContains(t, out, "dailyBudgetUSD: 200.0000")
	assertContains(t, out, "warnAtPct: 75")
}

func TestExport_K8s_DocumentSeparators(t *testing.T) {
	state := townctl.ExportState{
		Rigs: []townctl.DesiredRigRow{
			{Name: "r1", Repo: "/r1", Branch: "main", Enabled: true},
			{Name: "r2", Repo: "/r2", Branch: "main", Enabled: true},
		},
	}

	out := mustExport(t, state, townctl.ExportOptions{Backend: townctl.BackendK8s})

	count := strings.Count(out, "---")
	// One separator per rig (two rigs = two separators after GasTown CR).
	if count < 2 {
		t.Errorf("expected at least 2 document separators, got %d\noutput:\n%s", count, out)
	}
}
