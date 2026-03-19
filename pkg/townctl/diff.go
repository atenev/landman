// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements the diff and SQL generation for all desired_topology
// tables: desired_rigs, desired_agent_config, desired_formulas,
// desired_custom_roles, and desired_rig_custom_roles. It complements
// costpolicy.go which handles desired_cost_policy.
//
// SQL generation strategy (ADR-0001, Step 9):
//   - desired_custom_roles: upsert first (no FK dependencies on other topology
//     tables), then delete removed roles.
//   - desired_rigs: upsert all rigs, then delete absent rigs (CASCADE removes
//     dependent agent_config, formulas, and rig_custom_roles automatically).
//   - desired_agent_config: upsert per (rig, role); roles absent for a rig
//     that still exists are deleted explicitly.
//   - desired_formulas: upsert per (rig, name); absent formulas deleted.
//   - desired_rig_custom_roles: upsert per (rig, role_name); absent pairs deleted.
package townctl

import (
	"fmt"
	"strings"

	"github.com/tenev/dgt/pkg/manifest"
)

const (
	rigsSchemaVersion          = 1
	agentConfigSchemaVersion   = 1
	formulasSchemaVersion      = 1
	customRolesSchemaVersion   = 1
	rigCustomRolesSchemaVersion = 1
)

// TopologyTables lists all desired_topology tables written by ApplyTopologySQL.
var TopologyTables = []TableSchemaVersion{
	{Table: "desired_rigs", Version: rigsSchemaVersion},
	{Table: "desired_agent_config", Version: agentConfigSchemaVersion},
	{Table: "desired_formulas", Version: formulasSchemaVersion},
	{Table: "desired_custom_roles", Version: customRolesSchemaVersion},
	{Table: "desired_rig_custom_roles", Version: rigCustomRolesSchemaVersion},
}

// TopologyOp is a single planned operation for the dry-run output.
type TopologyOp struct {
	Action string // "add", "update", or "remove"
	Table  string
	Key    string // human-readable primary key description
	Detail string // field summary for updates
}

// ApplyTopologySQL generates the ordered SQL statements for a full topology
// apply transaction. It does NOT include desired_cost_policy statements
// (handled by costpolicy.ApplySQL). The caller wraps these in BEGIN/COMMIT.
//
// Statement order (ADR-0003 contract):
//  1. desired_topology_versions upsert for all topology tables touched.
//  2. desired_custom_roles upserts then cleanup.
//  3. desired_rigs upserts then cleanup (CASCADE handles dependents).
//  4. desired_agent_config upserts then per-rig role cleanup.
//  5. desired_formulas upserts then per-rig formula cleanup.
//  6. desired_rig_custom_roles upserts then cleanup.
func ApplyTopologySQL(m *manifest.TownManifest) []string {
	var stmts []string

	// 1. Versions upsert — must be first (ADR-0003).
	stmts = append(stmts, TopologyVersionsUpsert(TopologyTables))

	// 2. desired_custom_roles
	stmts = append(stmts, customRolesSQL(m)...)

	// 3. desired_rigs (CASCADE cleans dependents for deleted rigs)
	stmts = append(stmts, rigsSQL(m)...)

	// 4. desired_agent_config
	stmts = append(stmts, agentConfigSQL(m)...)

	// 5. desired_formulas
	stmts = append(stmts, formulasSQL(m)...)

	// 6. desired_rig_custom_roles
	stmts = append(stmts, rigCustomRolesSQL(m)...)

	return stmts
}

// FormatTopologyDryRun prints the planned topology operations in the
// +/~/- format used by town-ctl dry-run output.
func FormatTopologyDryRun(ops []TopologyOp) string {
	if len(ops) == 0 {
		return "desired_topology: no changes\n"
	}
	var b strings.Builder
	for _, op := range ops {
		switch op.Action {
		case "add":
			fmt.Fprintf(&b, "+ %s: %s\n", op.Table, op.Key)
		case "update":
			fmt.Fprintf(&b, "~ %s: %s %s\n", op.Table, op.Key, op.Detail)
		case "remove":
			fmt.Fprintf(&b, "- %s: %s\n", op.Table, op.Key)
		}
	}
	return b.String()
}

// ─── desired_custom_roles ────────────────────────────────────────────────────

func customRolesSQL(m *manifest.TownManifest) []string {
	var stmts []string
	roleNames := make([]string, 0, len(m.Roles))
	for _, role := range m.Roles {
		stmts = append(stmts, upsertCustomRole(role))
		roleNames = append(roleNames, role.Name)
	}
	stmts = append(stmts, deleteNotIn("desired_custom_roles", "name", roleNames))
	return stmts
}

func upsertCustomRole(r manifest.RoleSpec) string {
	lifespan := r.Lifespan
	if lifespan == "" {
		lifespan = "ephemeral"
	}
	maxInstances := r.Resources.MaxInstances
	if maxInstances == 0 {
		maxInstances = 1
	}
	reportsTo := "NULL"
	if r.Supervision.ReportsTo != "" {
		reportsTo = fmt.Sprintf("'%s'", escapeSQLString(r.Supervision.ReportsTo))
	}
	model := "NULL"
	if r.Identity.Model != "" {
		model = fmt.Sprintf("'%s'", escapeSQLString(r.Identity.Model))
	}
	triggerSchedule := "NULL"
	if r.Trigger.Schedule != "" {
		triggerSchedule = fmt.Sprintf("'%s'", escapeSQLString(r.Trigger.Schedule))
	}
	triggerEvent := "NULL"
	if r.Trigger.Event != "" {
		triggerEvent = fmt.Sprintf("'%s'", escapeSQLString(r.Trigger.Event))
	}
	return fmt.Sprintf(
		"INSERT INTO desired_custom_roles"+
			" (name, description, scope, lifespan, trigger_type, trigger_schedule,"+
			" trigger_event, claude_md_path, model, parent_role, reports_to, max_instances)"+
			" VALUES ('%s', '%s', '%s', '%s', '%s', %s, %s, '%s', %s, '%s', %s, %d)"+
			" ON DUPLICATE KEY UPDATE"+
			" description=VALUES(description), scope=VALUES(scope),"+
			" lifespan=VALUES(lifespan), trigger_type=VALUES(trigger_type),"+
			" trigger_schedule=VALUES(trigger_schedule), trigger_event=VALUES(trigger_event),"+
			" claude_md_path=VALUES(claude_md_path), model=VALUES(model),"+
			" parent_role=VALUES(parent_role), reports_to=VALUES(reports_to),"+
			" max_instances=VALUES(max_instances);",
		escapeSQLString(r.Name),
		escapeSQLString(r.Description),
		escapeSQLString(r.Scope),
		escapeSQLString(lifespan),
		escapeSQLString(r.Trigger.Type),
		triggerSchedule,
		triggerEvent,
		escapeSQLString(r.Identity.ClaudeMD),
		model,
		escapeSQLString(r.Supervision.Parent),
		reportsTo,
		maxInstances,
	)
}

// ─── desired_rigs ────────────────────────────────────────────────────────────

func rigsSQL(m *manifest.TownManifest) []string {
	var stmts []string
	rigNames := make([]string, 0, len(m.Rigs))
	for _, rig := range m.Rigs {
		stmts = append(stmts, upsertRig(rig))
		rigNames = append(rigNames, rig.Name)
	}
	stmts = append(stmts, deleteNotIn("desired_rigs", "name", rigNames))
	return stmts
}

func upsertRig(r manifest.RigSpec) string {
	return fmt.Sprintf(
		"INSERT INTO desired_rigs (name, repo, branch, enabled)"+
			" VALUES ('%s', '%s', '%s', %t)"+
			" ON DUPLICATE KEY UPDATE"+
			" repo=VALUES(repo), branch=VALUES(branch), enabled=VALUES(enabled);",
		escapeSQLString(r.Name),
		escapeSQLString(r.Repo),
		escapeSQLString(r.Branch),
		r.Enabled,
	)
}

// ─── desired_agent_config ────────────────────────────────────────────────────

// agentConfigRow represents one row in desired_agent_config.
type agentConfigRow struct {
	RigName     string
	Role        string
	Model       string // "" → NULL
	MaxPolecats int    // 0 → NULL
	ClaudeMDPath string // "" → NULL
}

func desiredAgentConfigRows(m *manifest.TownManifest) []agentConfigRow {
	var rows []agentConfigRow
	for _, rig := range m.Rigs {
		ac := rig.Agents
		mayorModel := ac.PolekatModel
		if mayorModel == "" {
			mayorModel = m.Defaults.MayorModel
		}
		polekatModel := ac.PolekatModel
		if polekatModel == "" {
			polekatModel = m.Defaults.PolekatModel
		}
		maxPolecats := ac.MaxPolecats
		if maxPolecats == 0 {
			maxPolecats = m.Defaults.MaxPolecats
		}

		if ac.Mayor {
			rows = append(rows, agentConfigRow{
				RigName:      rig.Name,
				Role:         "mayor",
				Model:        mayorModel,
				ClaudeMDPath: ac.MayorClaudeMD,
			})
		}
		if ac.Witness {
			rows = append(rows, agentConfigRow{
				RigName: rig.Name,
				Role:    "witness",
			})
		}
		if ac.Refinery {
			rows = append(rows, agentConfigRow{
				RigName: rig.Name,
				Role:    "refinery",
			})
		}
		if ac.Deacon {
			rows = append(rows, agentConfigRow{
				RigName: rig.Name,
				Role:    "deacon",
			})
		}
		// Polecat is always present if max_polecats > 0.
		if maxPolecats > 0 {
			rows = append(rows, agentConfigRow{
				RigName:     rig.Name,
				Role:        "polecat",
				Model:       polekatModel,
				MaxPolecats: maxPolecats,
			})
		}
	}
	return rows
}

func agentConfigSQL(m *manifest.TownManifest) []string {
	rows := desiredAgentConfigRows(m)
	var stmts []string

	// Build map of (rig_name, role) → row for cleanup.
	type rigRole struct{ rig, role string }
	desired := make(map[rigRole]struct{}, len(rows))

	for _, row := range rows {
		stmts = append(stmts, upsertAgentConfig(row))
		desired[rigRole{row.RigName, row.Role}] = struct{}{}
	}

	// Per-rig cleanup: delete roles not in desired set for that rig.
	// Group by rig first.
	rigDesiredRoles := make(map[string][]string)
	for rr := range desired {
		rigDesiredRoles[rr.rig] = append(rigDesiredRoles[rr.rig], rr.role)
	}
	for _, rig := range m.Rigs {
		roles := rigDesiredRoles[rig.Name]
		stmts = append(stmts, deleteAgentConfigExcept(rig.Name, roles))
	}
	return stmts
}

func upsertAgentConfig(r agentConfigRow) string {
	model := "NULL"
	if r.Model != "" {
		model = fmt.Sprintf("'%s'", escapeSQLString(r.Model))
	}
	maxPolecats := "NULL"
	if r.MaxPolecats > 0 {
		maxPolecats = fmt.Sprintf("%d", r.MaxPolecats)
	}
	claudeMD := "NULL"
	if r.ClaudeMDPath != "" {
		claudeMD = fmt.Sprintf("'%s'", escapeSQLString(r.ClaudeMDPath))
	}
	return fmt.Sprintf(
		"INSERT INTO desired_agent_config (rig_name, role, enabled, model, max_polecats, claude_md_path)"+
			" VALUES ('%s', '%s', TRUE, %s, %s, %s)"+
			" ON DUPLICATE KEY UPDATE"+
			" enabled=TRUE, model=VALUES(model), max_polecats=VALUES(max_polecats),"+
			" claude_md_path=VALUES(claude_md_path);",
		escapeSQLString(r.RigName),
		escapeSQLString(r.Role),
		model,
		maxPolecats,
		claudeMD,
	)
}

func deleteAgentConfigExcept(rigName string, keepRoles []string) string {
	if len(keepRoles) == 0 {
		return fmt.Sprintf(
			"DELETE FROM desired_agent_config WHERE rig_name = '%s';",
			escapeSQLString(rigName))
	}
	quoted := make([]string, len(keepRoles))
	for i, r := range keepRoles {
		quoted[i] = fmt.Sprintf("'%s'", escapeSQLString(r))
	}
	return fmt.Sprintf(
		"DELETE FROM desired_agent_config WHERE rig_name = '%s' AND role NOT IN (%s);",
		escapeSQLString(rigName),
		strings.Join(quoted, ", "),
	)
}

// ─── desired_formulas ────────────────────────────────────────────────────────

func formulasSQL(m *manifest.TownManifest) []string {
	var stmts []string
	type rigFormula struct{ rig, name string }
	desired := map[rigFormula]struct{}{}

	for _, rig := range m.Rigs {
		for _, f := range rig.Formulas {
			stmts = append(stmts, upsertFormula(rig.Name, f))
			desired[rigFormula{rig.Name, f.Name}] = struct{}{}
		}
	}

	// Per-rig formula cleanup.
	rigDesiredFormulas := make(map[string][]string)
	for rf := range desired {
		rigDesiredFormulas[rf.rig] = append(rigDesiredFormulas[rf.rig], rf.name)
	}
	for _, rig := range m.Rigs {
		formulas := rigDesiredFormulas[rig.Name]
		stmts = append(stmts, deleteFormulasExcept(rig.Name, formulas))
	}
	return stmts
}

func upsertFormula(rigName string, f manifest.FormulaRef) string {
	return fmt.Sprintf(
		"INSERT INTO desired_formulas (rig_name, name, schedule)"+
			" VALUES ('%s', '%s', '%s')"+
			" ON DUPLICATE KEY UPDATE schedule=VALUES(schedule);",
		escapeSQLString(rigName),
		escapeSQLString(f.Name),
		escapeSQLString(f.Schedule),
	)
}

func deleteFormulasExcept(rigName string, keepNames []string) string {
	if len(keepNames) == 0 {
		return fmt.Sprintf(
			"DELETE FROM desired_formulas WHERE rig_name = '%s';",
			escapeSQLString(rigName))
	}
	quoted := make([]string, len(keepNames))
	for i, n := range keepNames {
		quoted[i] = fmt.Sprintf("'%s'", escapeSQLString(n))
	}
	return fmt.Sprintf(
		"DELETE FROM desired_formulas WHERE rig_name = '%s' AND name NOT IN (%s);",
		escapeSQLString(rigName),
		strings.Join(quoted, ", "),
	)
}

// ─── desired_rig_custom_roles ────────────────────────────────────────────────

func rigCustomRolesSQL(m *manifest.TownManifest) []string {
	var stmts []string
	type rigRole struct{ rig, role string }
	var desired []rigRole

	// Build desired set: each rig's Agents.Roles list.
	for _, rig := range m.Rigs {
		for _, roleName := range rig.Agents.Roles {
			stmts = append(stmts, upsertRigCustomRole(rig.Name, roleName))
			desired = append(desired, rigRole{rig.Name, roleName})
		}
	}

	// Per-rig cleanup.
	rigDesired := make(map[string][]string)
	for _, rr := range desired {
		rigDesired[rr.rig] = append(rigDesired[rr.rig], rr.role)
	}
	for _, rig := range m.Rigs {
		roles := rigDesired[rig.Name]
		stmts = append(stmts, deleteRigCustomRolesExcept(rig.Name, roles))
	}
	return stmts
}

func upsertRigCustomRole(rigName, roleName string) string {
	return fmt.Sprintf(
		"INSERT INTO desired_rig_custom_roles (rig_name, role_name, enabled)"+
			" VALUES ('%s', '%s', TRUE)"+
			" ON DUPLICATE KEY UPDATE enabled=TRUE;",
		escapeSQLString(rigName),
		escapeSQLString(roleName),
	)
}

func deleteRigCustomRolesExcept(rigName string, keepRoles []string) string {
	if len(keepRoles) == 0 {
		return fmt.Sprintf(
			"DELETE FROM desired_rig_custom_roles WHERE rig_name = '%s';",
			escapeSQLString(rigName))
	}
	quoted := make([]string, len(keepRoles))
	for i, r := range keepRoles {
		quoted[i] = fmt.Sprintf("'%s'", escapeSQLString(r))
	}
	return fmt.Sprintf(
		"DELETE FROM desired_rig_custom_roles WHERE rig_name = '%s' AND role_name NOT IN (%s);",
		escapeSQLString(rigName),
		strings.Join(quoted, ", "),
	)
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// deleteNotIn returns a DELETE statement for rows in table where col NOT IN names.
// When names is empty, all rows are deleted.
func deleteNotIn(table, col string, names []string) string {
	if len(names) == 0 {
		return fmt.Sprintf("DELETE FROM %s;", table)
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("'%s'", escapeSQLString(n))
	}
	return fmt.Sprintf("DELETE FROM %s WHERE %s NOT IN (%s);",
		table, col, strings.Join(quoted, ", "))
}
