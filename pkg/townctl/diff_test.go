package townctl_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/townctl"
)

// joinStmtQueries joins the Query fields of a []Stmt slice for substring checks
// against SQL structure (table names, statement types). Values are in Args, not Query.
func joinStmtQueries(stmts []townctl.Stmt) string {
	qs := make([]string, len(stmts))
	for i, s := range stmts {
		qs[i] = s.Query
	}
	return strings.Join(qs, "\n")
}

// stmtArgsContain reports whether any Stmt in stmts has an arg equal to val.
func stmtArgsContain(stmts []townctl.Stmt, val any) bool {
	for _, s := range stmts {
		for _, a := range s.Args {
			if a == val {
				return true
			}
		}
	}
	return false
}

// ─── FullApplySQL ─────────────────────────────────────────────────────────────

func TestFullApplySQL_ContainsVersionsUpsert(t *testing.T) {
	m := mustParse(t, noPolicy)
	stmts := townctl.FullApplySQL(m)
	if len(stmts) == 0 {
		t.Fatal("expected at least 1 statement")
	}
	// Multiple version upserts are expected (one per SQL generator).
	any := false
	for _, s := range stmts {
		if strings.Contains(s.Query, "desired_topology_versions") {
			any = true
			break
		}
	}
	if !any {
		t.Error("expected at least one desired_topology_versions upsert in FullApplySQL output")
	}
}

func TestFullApplySQL_ContainsAllTopologyTables(t *testing.T) {
	m := mustParse(t, noPolicy)
	stmts := townctl.FullApplySQL(m)
	all := joinStmtQueries(stmts)
	for _, table := range []string{
		"desired_rigs",
		"desired_agent_config",
		"desired_formulas",
		"desired_custom_roles",
		"desired_rig_custom_roles",
	} {
		if !strings.Contains(all, table) {
			t.Errorf("expected reference to %q in FullApplySQL output", table)
		}
	}
}

// ─── TopologyApplySQL (from topology.go) ─────────────────────────────────────

func TestTopologyApplySQL_RigUpsertPresent(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"
enabled = true
`
	m := mustParse(t, toml)
	stmts := townctl.TopologyApplySQL(m)
	all := joinStmtQueries(stmts)
	// "backend" is a value (arg), not in the query template.
	if !stmtArgsContain(stmts, "backend") {
		t.Errorf("expected 'backend' rig in SQL args")
	}
	if !strings.Contains(all, "INSERT INTO desired_rigs") {
		t.Errorf("expected INSERT INTO desired_rigs statement")
	}
}

func TestTopologyApplySQL_DeleteNotInWhenNoRigs(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"
`
	m := mustParse(t, toml)
	stmts := townctl.TopologyApplySQL(m)
	all := joinStmtQueries(stmts)
	if !strings.Contains(all, "DELETE FROM desired_rigs") {
		t.Errorf("expected DELETE FROM desired_rigs when no rigs, got:\n%s", all)
	}
}

func TestTopologyApplySQL_AgentConfigMayorRow(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[defaults]
mayor_model = "claude-opus-4-6"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [rig.agents]
  mayor = true
`
	m := mustParse(t, toml)
	stmts := townctl.TopologyApplySQL(m)
	all := joinStmtQueries(stmts)
	if !strings.Contains(all, "INSERT INTO desired_agent_config") {
		t.Errorf("expected INSERT INTO desired_agent_config")
	}
	// "mayor" is a value arg, not embedded in the query template.
	if !stmtArgsContain(stmts, "mayor") {
		t.Errorf("expected 'mayor' role in agent config args")
	}
}

func TestTopologyApplySQL_FormulaUpsert(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [[rig.formula]]
  name     = "nightly"
  schedule = "0 2 * * *"
`
	m := mustParse(t, toml)
	stmts := townctl.TopologyApplySQL(m)
	all := joinStmtQueries(stmts)
	if !strings.Contains(all, "INSERT INTO desired_formulas") {
		t.Errorf("expected INSERT INTO desired_formulas")
	}
	if !stmtArgsContain(stmts, "nightly") {
		t.Errorf("expected formula name 'nightly' in args")
	}
}

// ─── CustomRolesApplySQL (from customroles.go) ───────────────────────────────

func TestCustomRolesApplySQL_RoleUpsertPresent(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

[[role]]
name  = "auditor"
scope = "rig"

  [role.identity]
  claude_md = "/tmp/auditor.md"

  [role.trigger]
  type = "manual"

  [role.supervision]
  parent = "mayor"
`
	m := mustParse(t, toml)
	stmts := townctl.CustomRolesApplySQL(m)
	all := joinStmtQueries(stmts)
	if !strings.Contains(all, "INSERT INTO desired_custom_roles") {
		t.Errorf("expected INSERT INTO desired_custom_roles")
	}
	if !stmtArgsContain(stmts, "auditor") {
		t.Errorf("expected role name 'auditor' in statement args")
	}
}

// ─── FormatTopologyDryRun ─────────────────────────────────────────────────────

func TestFormatTopologyDryRun_NoOps(t *testing.T) {
	out := townctl.FormatTopologyDryRun(nil)
	if !strings.Contains(out, "no changes") {
		t.Errorf("expected 'no changes', got %q", out)
	}
}

func TestFormatTopologyDryRun_AddPrefix(t *testing.T) {
	ops := []townctl.TopologyOp{{Action: "add", Table: "desired_rigs", Key: "name=backend"}}
	out := townctl.FormatTopologyDryRun(ops)
	if !strings.Contains(out, "+ desired_rigs: name=backend") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestFormatTopologyDryRun_RemovePrefix(t *testing.T) {
	ops := []townctl.TopologyOp{{Action: "remove", Table: "desired_rigs", Key: "name=old"}}
	out := townctl.FormatTopologyDryRun(ops)
	if !strings.Contains(out, "- desired_rigs") {
		t.Errorf("expected '- ' prefix for remove, got %q", out)
	}
}
