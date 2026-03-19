// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0004).
//
// This file implements the diff and write logic for custom roles (dgt-ytm):
// desired_custom_roles and desired_rig_custom_roles tables (migration 002).
package townctl

import (
	"fmt"
	"os"
	"strings"

	"github.com/tenev/dgt/pkg/manifest"
)

const (
	// customRolesSchemaVersion is the schema version written to
	// desired_topology_versions for desired_custom_roles (ADR-0003 contract).
	customRolesSchemaVersion = 1

	// rigCustomRolesSchemaVersion is the schema version for desired_rig_custom_roles.
	rigCustomRolesSchemaVersion = 1
)

// CustomRoleRow represents one row in the desired_custom_roles Dolt table.
type CustomRoleRow struct {
	Name            string
	Description     string
	Scope           string // "town" or "rig"
	Lifespan        string // "ephemeral" or "persistent"
	TriggerType     string // "bead_assigned", "schedule", "event", "manual"
	TriggerSchedule string // non-empty when TriggerType="schedule"
	TriggerEvent    string // non-empty when TriggerType="event"
	ClaudeMDPath    string
	Model           string // empty → inherit from rig defaults
	ParentRole      string
	ReportsTo       string // empty → same as ParentRole
	MaxInstances    int    // 0 → use DB default of 1
}

// RigCustomRoleRow represents one row in the desired_rig_custom_roles junction table.
type RigCustomRoleRow struct {
	RigName  string
	RoleName string
}

// ResolveCustomRoles converts all [[role]] entries in m into CustomRoleRow values.
// Claude MD paths are expanded via os.ExpandEnv.
func ResolveCustomRoles(m *manifest.TownManifest) []CustomRoleRow {
	rows := make([]CustomRoleRow, 0, len(m.Roles))
	for _, r := range m.Roles {
		rows = append(rows, roleSpecToRow(r))
	}
	return rows
}

// ResolveRigCustomRoles returns the set of (rig, role) opt-in pairs for
// scope=rig roles. Only rigs whose agents.roles list is non-empty produce rows.
func ResolveRigCustomRoles(m *manifest.TownManifest) []RigCustomRoleRow {
	var rows []RigCustomRoleRow
	for _, rig := range m.Rigs {
		for _, roleName := range rig.Agents.Roles {
			rows = append(rows, RigCustomRoleRow{
				RigName:  rig.Name,
				RoleName: roleName,
			})
		}
	}
	return rows
}

func roleSpecToRow(r manifest.RoleSpec) CustomRoleRow {
	lifespan := r.Lifespan
	if lifespan == "" {
		lifespan = "ephemeral"
	}
	maxInstances := r.Resources.MaxInstances
	if maxInstances == 0 {
		maxInstances = 1
	}
	return CustomRoleRow{
		Name:            r.Name,
		Description:     r.Description,
		Scope:           r.Scope,
		Lifespan:        lifespan,
		TriggerType:     r.Trigger.Type,
		TriggerSchedule: r.Trigger.Schedule,
		TriggerEvent:    r.Trigger.Event,
		ClaudeMDPath:    os.ExpandEnv(r.Identity.ClaudeMD),
		Model:           r.Identity.Model,
		ParentRole:      r.Supervision.Parent,
		ReportsTo:       r.Supervision.ReportsTo,
		MaxInstances:    maxInstances,
	}
}

// CustomRoleDiffOp describes a single planned operation on desired_custom_roles.
type CustomRoleDiffOp struct {
	// Action is "add", "update", or "remove".
	Action string
	Row    CustomRoleRow
}

// RigCustomRoleDiffOp describes a single planned operation on desired_rig_custom_roles.
type RigCustomRoleDiffOp struct {
	// Action is "add" or "remove".
	Action string
	Row    RigCustomRoleRow
}

// CustomRolesDiff holds the computed diff for both custom-role tables.
type CustomRolesDiff struct {
	RoleOps    []CustomRoleDiffOp
	RigRoleOps []RigCustomRoleDiffOp
}

// IsEmpty reports whether the diff has no operations.
func (d *CustomRolesDiff) IsEmpty() bool {
	return len(d.RoleOps) == 0 && len(d.RigRoleOps) == 0
}

// DiffCustomRoles computes the diff between the desired state (derived from m)
// and the current state (currentRoles, currentRigRoles read from Dolt).
// Callers may pass nil slices when no rows exist yet.
func DiffCustomRoles(
	m *manifest.TownManifest,
	currentRoles []CustomRoleRow,
	currentRigRoles []RigCustomRoleRow,
) *CustomRolesDiff {
	desired := ResolveCustomRoles(m)
	desiredRig := ResolveRigCustomRoles(m)

	diff := &CustomRolesDiff{}

	// ── desired_custom_roles diff ────────────────────────────────────────────
	currentByName := make(map[string]CustomRoleRow, len(currentRoles))
	for _, row := range currentRoles {
		currentByName[row.Name] = row
	}
	desiredNames := make(map[string]struct{}, len(desired))
	for _, row := range desired {
		desiredNames[row.Name] = struct{}{}
		if cur, exists := currentByName[row.Name]; !exists {
			diff.RoleOps = append(diff.RoleOps, CustomRoleDiffOp{Action: "add", Row: row})
		} else if cur != row {
			diff.RoleOps = append(diff.RoleOps, CustomRoleDiffOp{Action: "update", Row: row})
		}
	}
	for _, cur := range currentRoles {
		if _, keep := desiredNames[cur.Name]; !keep {
			diff.RoleOps = append(diff.RoleOps, CustomRoleDiffOp{Action: "remove", Row: cur})
		}
	}

	// ── desired_rig_custom_roles diff ────────────────────────────────────────
	type rigRoleKey struct{ rig, role string }
	currentRigByKey := make(map[rigRoleKey]struct{}, len(currentRigRoles))
	for _, row := range currentRigRoles {
		currentRigByKey[rigRoleKey{row.RigName, row.RoleName}] = struct{}{}
	}
	desiredRigKeys := make(map[rigRoleKey]struct{}, len(desiredRig))
	for _, row := range desiredRig {
		k := rigRoleKey{row.RigName, row.RoleName}
		desiredRigKeys[k] = struct{}{}
		if _, exists := currentRigByKey[k]; !exists {
			diff.RigRoleOps = append(diff.RigRoleOps, RigCustomRoleDiffOp{Action: "add", Row: row})
		}
	}
	for _, cur := range currentRigRoles {
		k := rigRoleKey{cur.RigName, cur.RoleName}
		if _, keep := desiredRigKeys[k]; !keep {
			diff.RigRoleOps = append(diff.RigRoleOps, RigCustomRoleDiffOp{Action: "remove", Row: cur})
		}
	}

	return diff
}

// CustomRolesApplySQL returns the ordered SQL statements for an atomic Dolt
// transaction that brings desired_custom_roles and desired_rig_custom_roles in
// sync with m.
//
// Statement order (ADR-0003 contract):
//  1. desired_topology_versions upsert — MUST be first
//  2. UPSERT rows in desired_custom_roles
//  3. DELETE removed roles from desired_custom_roles
//  4. UPSERT rows in desired_rig_custom_roles
//  5. DELETE removed rig opt-ins from desired_rig_custom_roles
//
// If the diff against current is empty (pass nil for both current slices), the
// function still generates the full UPSERT+DELETE set. Use DiffCustomRoles to
// check for a no-op before calling this.
func CustomRolesApplySQL(m *manifest.TownManifest) []string {
	desired := ResolveCustomRoles(m)
	desiredRig := ResolveRigCustomRoles(m)

	stmts := make([]string, 0, 3+len(desired)+len(desiredRig))

	// 1. ADR-0003: versions upsert first.
	stmts = append(stmts, TopologyVersionsUpsert([]TableSchemaVersion{
		{Table: "desired_custom_roles", Version: customRolesSchemaVersion},
		{Table: "desired_rig_custom_roles", Version: rigCustomRolesSchemaVersion},
	}))

	// 2. UPSERT desired_custom_roles rows.
	for _, row := range desired {
		stmts = append(stmts, upsertCustomRoleRow(row))
	}

	// 3. DELETE removed roles.
	stmts = append(stmts, deleteRemovedCustomRoles(desired))

	// 4. UPSERT desired_rig_custom_roles rows.
	for _, row := range desiredRig {
		stmts = append(stmts, upsertRigCustomRoleRow(row))
	}

	// 5. DELETE removed rig opt-ins.
	stmts = append(stmts, deleteRemovedRigCustomRoles(desiredRig))

	return stmts
}

func upsertCustomRoleRow(r CustomRoleRow) string {
	// NULL for optional string fields when empty.
	model := nullOrQuoted(r.Model)
	triggerSchedule := nullOrQuoted(r.TriggerSchedule)
	triggerEvent := nullOrQuoted(r.TriggerEvent)
	reportsTo := nullOrQuoted(r.ReportsTo)

	return fmt.Sprintf(
		"INSERT INTO desired_custom_roles"+
			" (name, description, scope, lifespan, trigger_type, trigger_schedule,"+
			" trigger_event, claude_md_path, model, parent_role, reports_to, max_instances)"+
			" VALUES ('%s', '%s', '%s', '%s', '%s', %s, %s, '%s', %s, '%s', %s, %d)"+
			" ON DUPLICATE KEY UPDATE"+
			" description = VALUES(description), scope = VALUES(scope),"+
			" lifespan = VALUES(lifespan), trigger_type = VALUES(trigger_type),"+
			" trigger_schedule = VALUES(trigger_schedule),"+
			" trigger_event = VALUES(trigger_event),"+
			" claude_md_path = VALUES(claude_md_path), model = VALUES(model),"+
			" parent_role = VALUES(parent_role), reports_to = VALUES(reports_to),"+
			" max_instances = VALUES(max_instances);",
		escapeSQLString(r.Name),
		escapeSQLString(r.Description),
		escapeSQLString(r.Scope),
		escapeSQLString(r.Lifespan),
		escapeSQLString(r.TriggerType),
		triggerSchedule,
		triggerEvent,
		escapeSQLString(r.ClaudeMDPath),
		model,
		escapeSQLString(r.ParentRole),
		reportsTo,
		r.MaxInstances,
	)
}

func upsertRigCustomRoleRow(r RigCustomRoleRow) string {
	return fmt.Sprintf(
		"INSERT INTO desired_rig_custom_roles (rig_name, role_name, enabled)"+
			" VALUES ('%s', '%s', TRUE)"+
			" ON DUPLICATE KEY UPDATE enabled = TRUE;",
		escapeSQLString(r.RigName),
		escapeSQLString(r.RoleName),
	)
}

func deleteRemovedCustomRoles(desired []CustomRoleRow) string {
	if len(desired) == 0 {
		return "DELETE FROM desired_custom_roles;"
	}
	quoted := make([]string, len(desired))
	for i, row := range desired {
		quoted[i] = fmt.Sprintf("'%s'", escapeSQLString(row.Name))
	}
	return fmt.Sprintf(
		"DELETE FROM desired_custom_roles WHERE name NOT IN (%s);",
		strings.Join(quoted, ", "),
	)
}

func deleteRemovedRigCustomRoles(desired []RigCustomRoleRow) string {
	if len(desired) == 0 {
		return "DELETE FROM desired_rig_custom_roles;"
	}
	pairs := make([]string, len(desired))
	for i, row := range desired {
		pairs[i] = fmt.Sprintf("('%s', '%s')",
			escapeSQLString(row.RigName), escapeSQLString(row.RoleName))
	}
	return fmt.Sprintf(
		"DELETE FROM desired_rig_custom_roles WHERE (rig_name, role_name) NOT IN (%s);",
		strings.Join(pairs, ", "),
	)
}

// nullOrQuoted returns "NULL" for an empty string and a single-quoted,
// escaped SQL string literal for a non-empty value.
func nullOrQuoted(s string) string {
	if s == "" {
		return "NULL"
	}
	return fmt.Sprintf("'%s'", escapeSQLString(s))
}

// FormatCustomRolesDryRun renders a CustomRolesDiff as human-readable text for
// stdout. Uses +/~/- prefixes matching the town-ctl dry-run convention (ADR-0001).
func FormatCustomRolesDryRun(diff *CustomRolesDiff) string {
	if diff.IsEmpty() {
		return "desired_custom_roles: no changes\ndesired_rig_custom_roles: no changes\n"
	}
	var b strings.Builder
	for _, op := range diff.RoleOps {
		r := op.Row
		switch op.Action {
		case "add":
			fmt.Fprintf(&b, "+ desired_custom_roles: name=%s scope=%s trigger_type=%s\n",
				r.Name, r.Scope, r.TriggerType)
		case "update":
			fmt.Fprintf(&b, "~ desired_custom_roles: name=%s scope=%s trigger_type=%s\n",
				r.Name, r.Scope, r.TriggerType)
		case "remove":
			fmt.Fprintf(&b, "- desired_custom_roles: name=%s\n", r.Name)
		}
	}
	for _, op := range diff.RigRoleOps {
		r := op.Row
		switch op.Action {
		case "add":
			fmt.Fprintf(&b, "+ desired_rig_custom_roles: rig_name=%s role_name=%s\n",
				r.RigName, r.RoleName)
		case "remove":
			fmt.Fprintf(&b, "- desired_rig_custom_roles: rig_name=%s role_name=%s\n",
				r.RigName, r.RoleName)
		}
	}
	return b.String()
}
