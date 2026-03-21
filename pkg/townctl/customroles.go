// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0004).
//
// This file implements the diff and write logic for custom roles (dgt-ytm):
// desired_custom_roles and desired_rig_custom_roles tables (migration 002).
package townctl

import (
	"fmt"
	"os"
	"path/filepath"
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
	// ClaudeMDPath is the path stored in Dolt. When the role declares extends,
	// this is the path to the apply-time merged file (ADR-0005), not the
	// role's own claude_md path.
	ClaudeMDPath string
	Model        string // empty → inherit from rig defaults
	ParentRole   string
	ReportsTo    string // empty → same as ParentRole
	MaxInstances int    // 0 → use DB default of 1
	// ExtendsRole is the name of the custom role this role extends (ADR-0005).
	// Empty when no extends is declared. Stored for auditability; the Surveyor
	// uses ClaudeMDPath directly and does not need to re-merge.
	ExtendsRole string
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

// MergeAndWriteExtendsChains resolves the extends chain for each [[role]] that
// declares identity.extends, merges all CLAUDE.md files in the chain, and
// writes the merged content to <gtHome>/roles/merged/<name>.md (ADR-0005).
//
// The role's identity.ClaudeMD is updated in-place to the merged file path so
// that subsequent SQL generation (via ResolveCustomRoles / roleSpecToRow) stores
// the correct merged path in desired_custom_roles.claude_md_path.
//
// Roles without extends are not modified. gtHome is expanded via os.ExpandEnv
// before use.
func MergeAndWriteExtendsChains(m *manifest.TownManifest, gtHome string) error {
	mergedDir := filepath.Join(os.ExpandEnv(gtHome), "roles", "merged")

	for i, role := range m.Roles {
		if role.Identity.Extends == "" {
			continue
		}

		chain, err := manifest.ResolveExtendsChain(role.Name, m.Roles)
		if err != nil {
			return fmt.Errorf("role %q: resolve extends chain: %w", role.Name, err)
		}

		merged, err := manifest.MergeExtendsChain(chain)
		if err != nil {
			return fmt.Errorf("role %q: merge extends chain: %w", role.Name, err)
		}

		if err := os.MkdirAll(mergedDir, 0o755); err != nil {
			return fmt.Errorf("role %q: create merged dir %s: %w", role.Name, mergedDir, err)
		}

		mergedPath := filepath.Join(mergedDir, role.Name+".md")
		if err := os.WriteFile(mergedPath, []byte(merged), 0o644); err != nil {
			return fmt.Errorf("role %q: write merged CLAUDE.md %s: %w", role.Name, mergedPath, err)
		}

		m.Roles[i].Identity.ClaudeMD = mergedPath
	}
	return nil
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
		ExtendsRole:     r.Identity.Extends,
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
func CustomRolesApplySQL(m *manifest.TownManifest) []Stmt {
	desired := ResolveCustomRoles(m)
	desiredRig := ResolveRigCustomRoles(m)

	stmts := make([]Stmt, 0, 3+len(desired)+len(desiredRig))

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

func upsertCustomRoleRow(r CustomRoleRow) Stmt {
	// Convert empty optional strings to nil (SQL NULL).
	var model, triggerSchedule, triggerEvent, reportsTo, extendsRole any
	if r.Model != "" {
		model = r.Model
	}
	if r.TriggerSchedule != "" {
		triggerSchedule = r.TriggerSchedule
	}
	if r.TriggerEvent != "" {
		triggerEvent = r.TriggerEvent
	}
	if r.ReportsTo != "" {
		reportsTo = r.ReportsTo
	}
	if r.ExtendsRole != "" {
		extendsRole = r.ExtendsRole
	}
	return Stmt{
		Query: "INSERT INTO desired_custom_roles" +
			" (name, description, scope, lifespan, trigger_type, trigger_schedule," +
			" trigger_event, claude_md_path, model, parent_role, reports_to, max_instances, extends_role)" +
			" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)" +
			" ON DUPLICATE KEY UPDATE" +
			" description = VALUES(description), scope = VALUES(scope)," +
			" lifespan = VALUES(lifespan), trigger_type = VALUES(trigger_type)," +
			" trigger_schedule = VALUES(trigger_schedule)," +
			" trigger_event = VALUES(trigger_event)," +
			" claude_md_path = VALUES(claude_md_path), model = VALUES(model)," +
			" parent_role = VALUES(parent_role), reports_to = VALUES(reports_to)," +
			" max_instances = VALUES(max_instances), extends_role = VALUES(extends_role);",
		Args: []any{
			r.Name, r.Description, r.Scope, r.Lifespan, r.TriggerType,
			triggerSchedule, triggerEvent, r.ClaudeMDPath, model, r.ParentRole,
			reportsTo, r.MaxInstances, extendsRole,
		},
	}
}

func upsertRigCustomRoleRow(r RigCustomRoleRow) Stmt {
	return Stmt{
		Query: "INSERT INTO desired_rig_custom_roles (rig_name, role_name, enabled)" +
			" VALUES (?, ?, TRUE)" +
			" ON DUPLICATE KEY UPDATE enabled = TRUE;",
		Args: []any{r.RigName, r.RoleName},
	}
}

func deleteRemovedCustomRoles(desired []CustomRoleRow) Stmt {
	if len(desired) == 0 {
		return Stmt{Query: "DELETE FROM desired_custom_roles;"}
	}
	placeholders := strings.Repeat("?, ", len(desired))
	placeholders = placeholders[:len(placeholders)-2]
	args := make([]any, len(desired))
	for i, row := range desired {
		args[i] = row.Name
	}
	return Stmt{
		Query: fmt.Sprintf(
			"DELETE FROM desired_custom_roles WHERE name NOT IN (%s);",
			placeholders),
		Args: args,
	}
}

func deleteRemovedRigCustomRoles(desired []RigCustomRoleRow) Stmt {
	if len(desired) == 0 {
		return Stmt{Query: "DELETE FROM desired_rig_custom_roles;"}
	}
	pairPlaceholders := strings.Repeat("(?, ?), ", len(desired))
	pairPlaceholders = pairPlaceholders[:len(pairPlaceholders)-2]
	args := make([]any, 0, len(desired)*2)
	for _, row := range desired {
		args = append(args, row.RigName, row.RoleName)
	}
	return Stmt{
		Query: fmt.Sprintf(
			"DELETE FROM desired_rig_custom_roles WHERE (rig_name, role_name) NOT IN (%s);",
			pairPlaceholders),
		Args: args,
	}
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
