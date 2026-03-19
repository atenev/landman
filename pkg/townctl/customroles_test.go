package townctl_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

// ── helper ─────────────────────────────────────────────────────────────────

// customRolesBase is a minimal town manifest with no roles or rigs; tests
// append TOML snippets to build specific scenarios.
const customRolesBase = `
version = "1"

[town]
name = "t"
home = "/opt/gt"
`

func mustParseRoles(t *testing.T, tomlStr string) *manifest.TownManifest {
	t.Helper()
	m, err := manifest.Parse([]byte(tomlStr))
	if err != nil {
		t.Fatalf("manifest.Parse: %v", err)
	}
	return m
}

// ── ResolveCustomRoles ──────────────────────────────────────────────────────

func TestResolveCustomRoles_Empty(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveCustomRoles(m)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for manifest with no roles, got %d", len(rows))
	}
}

func TestResolveCustomRoles_MinimalRole(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveCustomRoles(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.Name != "reviewer" {
		t.Errorf("Name = %q, want %q", r.Name, "reviewer")
	}
	if r.Scope != "rig" {
		t.Errorf("Scope = %q, want %q", r.Scope, "rig")
	}
	if r.TriggerType != "bead_assigned" {
		t.Errorf("TriggerType = %q, want %q", r.TriggerType, "bead_assigned")
	}
	if r.ParentRole != "witness" {
		t.Errorf("ParentRole = %q, want %q", r.ParentRole, "witness")
	}
	// Default lifespan when omitted must be "ephemeral".
	if r.Lifespan != "ephemeral" {
		t.Errorf("Lifespan = %q, want %q (default)", r.Lifespan, "ephemeral")
	}
	// Default max_instances when omitted must be 1.
	if r.MaxInstances != 1 {
		t.Errorf("MaxInstances = %d, want 1 (default)", r.MaxInstances)
	}
}

func TestResolveCustomRoles_ScheduleTrigger(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "scanner"
scope = "town"

  [role.identity]
  claude_md = "/opt/gt/roles/scanner/CLAUDE.md"

  [role.trigger]
  type     = "schedule"
  schedule = "0 3 * * *"

  [role.supervision]
  parent = "deacon"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveCustomRoles(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].TriggerType != "schedule" {
		t.Errorf("TriggerType = %q, want schedule", rows[0].TriggerType)
	}
	if rows[0].TriggerSchedule != "0 3 * * *" {
		t.Errorf("TriggerSchedule = %q, want %q", rows[0].TriggerSchedule, "0 3 * * *")
	}
}

func TestResolveCustomRoles_EventTrigger(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "pr-checker"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/pr-checker/CLAUDE.md"

  [role.trigger]
  type  = "event"
  event = "pull_request.opened"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveCustomRoles(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].TriggerEvent != "pull_request.opened" {
		t.Errorf("TriggerEvent = %q, want pull_request.opened", rows[0].TriggerEvent)
	}
}

func TestResolveCustomRoles_AllTriggerTypesValid(t *testing.T) {
	triggers := []struct {
		triggerType string
		extra       string
	}{
		{"bead_assigned", ""},
		{"manual", ""},
		{"schedule", `schedule = "0 1 * * *"`},
		{"event", `event = "pr.opened"`},
	}

	for _, tc := range triggers {
		tc := tc
		t.Run(tc.triggerType, func(t *testing.T) {
			tomlStr := baseManifest + `
[[role]]
name  = "testrole"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/testrole/CLAUDE.md"

  [role.trigger]
  type = "` + tc.triggerType + `"
` + tc.extra + `

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`
			m := mustParseRoles(t, tomlStr)
			rows := townctl.ResolveCustomRoles(m)
			if len(rows) != 1 || rows[0].TriggerType != tc.triggerType {
				t.Errorf("trigger type %q: got rows %+v", tc.triggerType, rows)
			}
		})
	}
}

func TestResolveCustomRoles_ReportsTo(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "town"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent     = "mayor"
  reports_to = "deacon"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveCustomRoles(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].ReportsTo != "deacon" {
		t.Errorf("ReportsTo = %q, want deacon", rows[0].ReportsTo)
	}
}

func TestResolveCustomRoles_MaxInstances(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "scaling-agent"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/scaling-agent/CLAUDE.md"

  [role.trigger]
  type     = "schedule"
  schedule = "*/5 * * * *"

  [role.supervision]
  parent = "deacon"

  [role.resources]
  max_instances = 3

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveCustomRoles(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].MaxInstances != 3 {
		t.Errorf("MaxInstances = %d, want 3", rows[0].MaxInstances)
	}
}

// ── ResolveRigCustomRoles ───────────────────────────────────────────────────

func TestResolveRigCustomRoles_Empty_WhenNoOptIns(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	rows := townctl.ResolveRigCustomRoles(m)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows (no rig opt-ins), got %d", len(rows))
	}
}

func TestResolveRigCustomRoles_OptInProducesRows(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.agents]
  roles = ["reviewer"]

[[rig]]
name   = "docs"
repo   = "/srv/docs"
branch = "main"
`)
	rows := townctl.ResolveRigCustomRoles(m)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (backend opts in, docs does not), got %d: %+v", len(rows), rows)
	}
	if rows[0].RigName != "backend" || rows[0].RoleName != "reviewer" {
		t.Errorf("row = %+v, want {backend reviewer}", rows[0])
	}
}

func TestResolveRigCustomRoles_MultipleRigsMultipleRoles(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[role]]
name  = "scanner"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/scanner/CLAUDE.md"

  [role.trigger]
  type     = "schedule"
  schedule = "0 2 * * *"

  [role.supervision]
  parent = "deacon"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.agents]
  roles = ["reviewer", "scanner"]

[[rig]]
name   = "frontend"
repo   = "/srv/frontend"
branch = "main"

  [rig.agents]
  roles = ["reviewer"]
`)
	rows := townctl.ResolveRigCustomRoles(m)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %+v", len(rows), rows)
	}
}

// ── DiffCustomRoles ─────────────────────────────────────────────────────────

func TestDiffCustomRoles_AddRole_WhenCurrentEmpty(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	diff := townctl.DiffCustomRoles(m, nil, nil)
	if len(diff.RoleOps) != 1 || diff.RoleOps[0].Action != "add" {
		t.Errorf("expected 1 add op, got %+v", diff.RoleOps)
	}
	if diff.RoleOps[0].Row.Name != "reviewer" {
		t.Errorf("expected row name reviewer, got %q", diff.RoleOps[0].Row.Name)
	}
}

func TestDiffCustomRoles_RemoveRole_WhenRemovedFromManifest(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	currentRoles := []townctl.CustomRoleRow{
		{
			Name: "old-role", Scope: "rig", Lifespan: "ephemeral",
			TriggerType: "bead_assigned", ClaudeMDPath: "/opt/gt/roles/old-role/CLAUDE.md",
			ParentRole: "witness", MaxInstances: 1,
		},
	}
	diff := townctl.DiffCustomRoles(m, currentRoles, nil)
	if len(diff.RoleOps) != 1 || diff.RoleOps[0].Action != "remove" {
		t.Errorf("expected 1 remove op, got %+v", diff.RoleOps)
	}
	if diff.RoleOps[0].Row.Name != "old-role" {
		t.Errorf("expected row name old-role, got %q", diff.RoleOps[0].Row.Name)
	}
}

func TestDiffCustomRoles_UpdateRole_WhenFieldChanged(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

  [role.resources]
  max_instances = 2

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	// Current state has max_instances=1; desired has 2 → update.
	currentRoles := []townctl.CustomRoleRow{
		{
			Name: "reviewer", Scope: "rig", Lifespan: "ephemeral",
			TriggerType: "bead_assigned", ClaudeMDPath: "/opt/gt/roles/reviewer/CLAUDE.md",
			ParentRole: "witness", MaxInstances: 1,
		},
	}
	diff := townctl.DiffCustomRoles(m, currentRoles, nil)
	if len(diff.RoleOps) != 1 || diff.RoleOps[0].Action != "update" {
		t.Errorf("expected 1 update op, got %+v", diff.RoleOps)
	}
}

func TestDiffCustomRoles_NoChange_Idempotent(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	// Current state exactly matches desired state.
	currentRoles := []townctl.CustomRoleRow{
		{
			Name: "reviewer", Scope: "rig", Lifespan: "ephemeral",
			TriggerType: "bead_assigned", ClaudeMDPath: "/opt/gt/roles/reviewer/CLAUDE.md",
			ParentRole: "witness", MaxInstances: 1,
		},
	}
	diff := townctl.DiffCustomRoles(m, currentRoles, nil)
	if !diff.IsEmpty() {
		t.Errorf("expected empty diff (idempotent), got RoleOps=%+v RigRoleOps=%+v",
			diff.RoleOps, diff.RigRoleOps)
	}
}

func TestDiffCustomRoles_AddRigOptIn_WhenCurrentEmpty(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.agents]
  roles = ["reviewer"]
`)
	diff := townctl.DiffCustomRoles(m, nil, nil)
	if len(diff.RigRoleOps) != 1 || diff.RigRoleOps[0].Action != "add" {
		t.Errorf("expected 1 add rig opt-in, got %+v", diff.RigRoleOps)
	}
	if diff.RigRoleOps[0].Row.RigName != "backend" {
		t.Errorf("RigName = %q, want backend", diff.RigRoleOps[0].Row.RigName)
	}
}

func TestDiffCustomRoles_RemoveRigOptIn_WhenRemovedFromManifest(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"
`)
	// Rig had an opt-in; now the manifest no longer has roles in rig.agents.
	currentRigRoles := []townctl.RigCustomRoleRow{
		{RigName: "backend", RoleName: "reviewer"},
	}
	diff := townctl.DiffCustomRoles(m, nil, currentRigRoles)
	if len(diff.RigRoleOps) != 1 || diff.RigRoleOps[0].Action != "remove" {
		t.Errorf("expected 1 remove rig opt-in, got %+v", diff.RigRoleOps)
	}
}

// ── CustomRolesApplySQL ─────────────────────────────────────────────────────

func TestCustomRolesApplySQL_FirstStatementIsVersionsUpsert(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	stmts := townctl.CustomRolesApplySQL(m)
	if len(stmts) < 1 {
		t.Fatal("expected at least 1 statement")
	}
	if !strings.Contains(stmts[0], "desired_topology_versions") {
		t.Errorf("first statement must upsert desired_topology_versions, got: %s", stmts[0])
	}
	if !strings.Contains(stmts[0], "desired_custom_roles") {
		t.Errorf("first statement must reference desired_custom_roles, got: %s", stmts[0])
	}
	if !strings.Contains(stmts[0], "desired_rig_custom_roles") {
		t.Errorf("first statement must reference desired_rig_custom_roles, got: %s", stmts[0])
	}
}

func TestCustomRolesApplySQL_UpsertRoleRow(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	stmts := townctl.CustomRolesApplySQL(m)
	// Find an UPSERT statement for desired_custom_roles.
	var upsertFound bool
	for _, s := range stmts {
		if strings.Contains(s, "INSERT INTO desired_custom_roles") &&
			strings.Contains(s, "reviewer") {
			upsertFound = true
			break
		}
	}
	if !upsertFound {
		t.Errorf("expected an INSERT INTO desired_custom_roles for reviewer; statements: %v", stmts)
	}
}

func TestCustomRolesApplySQL_DeleteRemovedRoles(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	stmts := townctl.CustomRolesApplySQL(m)
	// The DELETE statement must include NOT IN with the desired role names.
	var deleteFound bool
	for _, s := range stmts {
		if strings.Contains(s, "DELETE FROM desired_custom_roles") &&
			strings.Contains(s, "NOT IN") &&
			strings.Contains(s, "reviewer") {
			deleteFound = true
			break
		}
	}
	if !deleteFound {
		t.Errorf("expected DELETE FROM desired_custom_roles NOT IN (reviewer); statements: %v", stmts)
	}
}

func TestCustomRolesApplySQL_DeleteAllRoles_WhenNoRoles(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	stmts := townctl.CustomRolesApplySQL(m)
	var deleteAll bool
	for _, s := range stmts {
		if s == "DELETE FROM desired_custom_roles;" {
			deleteAll = true
			break
		}
	}
	if !deleteAll {
		t.Errorf("expected 'DELETE FROM desired_custom_roles;' when no roles; statements: %v", stmts)
	}
}

func TestCustomRolesApplySQL_UpsertRigOptIn(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.agents]
  roles = ["reviewer"]
`)
	stmts := townctl.CustomRolesApplySQL(m)
	var rigUpsertFound bool
	for _, s := range stmts {
		if strings.Contains(s, "INSERT INTO desired_rig_custom_roles") &&
			strings.Contains(s, "backend") &&
			strings.Contains(s, "reviewer") {
			rigUpsertFound = true
			break
		}
	}
	if !rigUpsertFound {
		t.Errorf("expected INSERT INTO desired_rig_custom_roles for (backend, reviewer); statements: %v", stmts)
	}
}

func TestCustomRolesApplySQL_DeleteRemovedRigOptIns(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.agents]
  roles = ["reviewer"]
`)
	stmts := townctl.CustomRolesApplySQL(m)
	var deleteFound bool
	for _, s := range stmts {
		if strings.Contains(s, "DELETE FROM desired_rig_custom_roles") &&
			strings.Contains(s, "NOT IN") {
			deleteFound = true
			break
		}
	}
	if !deleteFound {
		t.Errorf("expected DELETE FROM desired_rig_custom_roles NOT IN ...; statements: %v", stmts)
	}
}

func TestCustomRolesApplySQL_DeleteAllRigOptIns_WhenNoOptIns(t *testing.T) {
	m := mustParseRoles(t, customRolesBase+`
[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	stmts := townctl.CustomRolesApplySQL(m)
	var deleteAll bool
	for _, s := range stmts {
		if s == "DELETE FROM desired_rig_custom_roles;" {
			deleteAll = true
			break
		}
	}
	if !deleteAll {
		t.Errorf("expected 'DELETE FROM desired_rig_custom_roles;' when no opt-ins; statements: %v", stmts)
	}
}

// ── FormatCustomRolesDryRun ─────────────────────────────────────────────────

func TestFormatCustomRolesDryRun_NoChanges(t *testing.T) {
	diff := &townctl.CustomRolesDiff{}
	out := townctl.FormatCustomRolesDryRun(diff)
	if !strings.Contains(out, "no changes") {
		t.Errorf("expected 'no changes' in output, got: %q", out)
	}
}

func TestFormatCustomRolesDryRun_AddPrefix(t *testing.T) {
	diff := &townctl.CustomRolesDiff{
		RoleOps: []townctl.CustomRoleDiffOp{
			{Action: "add", Row: townctl.CustomRoleRow{Name: "new-role", Scope: "rig", TriggerType: "bead_assigned"}},
		},
	}
	out := townctl.FormatCustomRolesDryRun(diff)
	if !strings.Contains(out, "+ desired_custom_roles") {
		t.Errorf("expected '+' prefix for add, got: %q", out)
	}
	if !strings.Contains(out, "new-role") {
		t.Errorf("expected role name in output, got: %q", out)
	}
}

func TestFormatCustomRolesDryRun_UpdatePrefix(t *testing.T) {
	diff := &townctl.CustomRolesDiff{
		RoleOps: []townctl.CustomRoleDiffOp{
			{Action: "update", Row: townctl.CustomRoleRow{Name: "existing-role", Scope: "town", TriggerType: "manual"}},
		},
	}
	out := townctl.FormatCustomRolesDryRun(diff)
	if !strings.Contains(out, "~ desired_custom_roles") {
		t.Errorf("expected '~' prefix for update, got: %q", out)
	}
}

func TestFormatCustomRolesDryRun_RemovePrefix(t *testing.T) {
	diff := &townctl.CustomRolesDiff{
		RoleOps: []townctl.CustomRoleDiffOp{
			{Action: "remove", Row: townctl.CustomRoleRow{Name: "gone-role"}},
		},
	}
	out := townctl.FormatCustomRolesDryRun(diff)
	if !strings.Contains(out, "- desired_custom_roles") {
		t.Errorf("expected '-' prefix for remove, got: %q", out)
	}
}

func TestFormatCustomRolesDryRun_RigOptInAddPrefix(t *testing.T) {
	diff := &townctl.CustomRolesDiff{
		RigRoleOps: []townctl.RigCustomRoleDiffOp{
			{Action: "add", Row: townctl.RigCustomRoleRow{RigName: "backend", RoleName: "reviewer"}},
		},
	}
	out := townctl.FormatCustomRolesDryRun(diff)
	if !strings.Contains(out, "+ desired_rig_custom_roles") {
		t.Errorf("expected '+' prefix for rig opt-in add, got: %q", out)
	}
	if !strings.Contains(out, "backend") || !strings.Contains(out, "reviewer") {
		t.Errorf("expected rig and role names in output, got: %q", out)
	}
}

func TestFormatCustomRolesDryRun_RigOptInRemovePrefix(t *testing.T) {
	diff := &townctl.CustomRolesDiff{
		RigRoleOps: []townctl.RigCustomRoleDiffOp{
			{Action: "remove", Row: townctl.RigCustomRoleRow{RigName: "backend", RoleName: "old-role"}},
		},
	}
	out := townctl.FormatCustomRolesDryRun(diff)
	if !strings.Contains(out, "- desired_rig_custom_roles") {
		t.Errorf("expected '-' prefix for rig opt-in remove, got: %q", out)
	}
}
