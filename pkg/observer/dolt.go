// Package observer - dolt.go
//
// Implements ReadTopology, which reads desired_* and actual_* tables from Dolt
// and maps them to the surveyor topology structs consumed by ComputeScore.
// Per ADR-0011 D4, each table group is queried independently: a failure in
// one group leaves that part of the snapshot empty without blocking others.
package observer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tenev/dgt/pkg/surveyor"
)

// TopologySnapshot holds a point-in-time read of desired + actual topology
// from Dolt. Fields may be partially populated when some query groups fail;
// callers should inspect the returned error before using the snapshot.
type TopologySnapshot struct {
	Desired surveyor.DesiredTopology
	Actual  surveyor.ActualTopology
	ReadAt  time.Time
}

// ReadTopology queries desired_* and actual_* tables from Dolt and returns a
// TopologySnapshot. Each table group is queried independently. A failure in
// one group leaves that part of the snapshot at its zero value and is
// collected into the returned error (errors.Join) without preventing other
// groups from being read. If all groups succeed the returned error is nil.
func ReadTopology(ctx context.Context, db *sql.DB) (TopologySnapshot, error) {
	snap := TopologySnapshot{ReadAt: time.Now()}
	var errs []error

	// --- Group 1: desired rigs (desired_rigs + desired_agent_config) ---
	desiredRigs, err := readDesiredRigs(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("desired_rigs: %w", err))
	} else {
		snap.Desired.Rigs = desiredRigs
	}

	// --- Group 2: desired custom roles ---
	desiredRoles, err := readDesiredCustomRoles(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("desired_custom_roles: %w", err))
	} else {
		snap.Desired.CustomRoles = desiredRoles
	}

	// --- Group 3: desired formulas ---
	desiredFormulas, err := readDesiredFormulas(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("desired_formulas: %w", err))
	} else {
		snap.Desired.Formulas = desiredFormulas
	}

	// --- Group 4: actual rigs ---
	actualRigs, err := readActualRigs(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("actual_rigs: %w", err))
	} else {
		snap.Actual.Rigs = actualRigs
	}

	// --- Group 5: actual agents ---
	actualAgents, err := readActualAgents(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("actual_agent_config: %w", err))
	} else {
		snap.Actual.Agents = actualAgents
	}

	// --- Group 6: actual worktrees ---
	actualWorktrees, err := readActualWorktrees(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("actual_worktrees: %w", err))
	} else {
		snap.Actual.Worktrees = actualWorktrees
	}

	// --- Group 7: actual custom roles ---
	actualRoles, err := readActualCustomRoles(ctx, db)
	if err != nil {
		errs = append(errs, fmt.Errorf("actual_custom_roles: %w", err))
	} else {
		snap.Actual.CustomRoles = actualRoles
	}

	return snap, errors.Join(errs...)
}

// readDesiredRigs fetches desired_rigs and desired_agent_config and merges
// them into []surveyor.DesiredRig. The agent-config query failure is
// non-fatal within this group: rigs are returned without pool/witness config.
func readDesiredRigs(ctx context.Context, db *sql.DB) ([]surveyor.DesiredRig, error) {
	// Query 1: base rig rows.
	rows, err := db.QueryContext(ctx, `SELECT name, enabled FROM desired_rigs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rigsByName := make(map[string]*surveyor.DesiredRig)
	var ordered []string
	for rows.Next() {
		var dr surveyor.DesiredRig
		if err := rows.Scan(&dr.Name, &dr.Enabled); err != nil {
			return nil, err
		}
		rigsByName[dr.Name] = &dr
		ordered = append(ordered, dr.Name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Query 2: polecat and witness agent config (non-fatal if unavailable).
	arows, err := db.QueryContext(ctx,
		`SELECT rig_name, role, enabled, max_polecats
		   FROM desired_agent_config
		  WHERE role IN ('polecat', 'witness')`)
	if err == nil {
		defer arows.Close()
		for arows.Next() {
			var rigName, role string
			var enabled bool
			var maxPolecats sql.NullInt64
			if err := arows.Scan(&rigName, &role, &enabled, &maxPolecats); err != nil {
				continue
			}
			dr, ok := rigsByName[rigName]
			if !ok {
				continue
			}
			switch role {
			case "polecat":
				if enabled && maxPolecats.Valid {
					dr.MaxPolecats = int(maxPolecats.Int64)
				}
			case "witness":
				if enabled {
					dr.WitnessEnabled = true
				}
			}
		}
		// Ignore arows.Err(): partial agent config is acceptable.
	}

	result := make([]surveyor.DesiredRig, 0, len(ordered))
	for _, name := range ordered {
		result = append(result, *rigsByName[name])
	}
	return result, nil
}

// readDesiredCustomRoles fetches desired_custom_roles and
// desired_rig_custom_roles, expanding max_instances into individual
// DesiredCustomRole entries (one per instance index).
func readDesiredCustomRoles(ctx context.Context, db *sql.DB) ([]surveyor.DesiredCustomRole, error) {
	// Town-scoped roles.
	trows, err := db.QueryContext(ctx,
		`SELECT name, max_instances
		   FROM desired_custom_roles
		  WHERE scope = 'town'`)
	if err != nil {
		return nil, err
	}
	defer trows.Close()

	var result []surveyor.DesiredCustomRole
	for trows.Next() {
		var name string
		var maxInstances int
		if err := trows.Scan(&name, &maxInstances); err != nil {
			return nil, err
		}
		for i := 0; i < maxInstances; i++ {
			result = append(result, surveyor.DesiredCustomRole{
				Name:          name,
				Scope:         "town",
				InstanceIndex: i,
			})
		}
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}

	// Rig-scoped roles (enabled bindings only).
	rrows, err := db.QueryContext(ctx,
		`SELECT cr.name, rcr.rig_name, cr.max_instances
		   FROM desired_custom_roles cr
		   JOIN desired_rig_custom_roles rcr ON rcr.role_name = cr.name
		  WHERE cr.scope = 'rig' AND rcr.enabled = TRUE`)
	if err != nil {
		// Rig role failure is non-fatal; return town roles.
		return result, nil
	}
	defer rrows.Close()

	for rrows.Next() {
		var name, rigName string
		var maxInstances int
		if err := rrows.Scan(&name, &rigName, &maxInstances); err != nil {
			return nil, err
		}
		for i := 0; i < maxInstances; i++ {
			result = append(result, surveyor.DesiredCustomRole{
				Name:          name,
				Scope:         "rig",
				RigName:       rigName,
				InstanceIndex: i,
			})
		}
	}
	return result, rrows.Err()
}

// readDesiredFormulas fetches the desired_formulas table.
func readDesiredFormulas(ctx context.Context, db *sql.DB) ([]surveyor.DesiredFormula, error) {
	rows, err := db.QueryContext(ctx, `SELECT rig_name, name FROM desired_formulas`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []surveyor.DesiredFormula
	for rows.Next() {
		var df surveyor.DesiredFormula
		if err := rows.Scan(&df.RigName, &df.Name); err != nil {
			return nil, err
		}
		result = append(result, df)
	}
	return result, rows.Err()
}

// readActualRigs fetches the actual_rigs table.
func readActualRigs(ctx context.Context, db *sql.DB) ([]surveyor.RigState, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name, enabled, status, last_seen FROM actual_rigs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []surveyor.RigState
	for rows.Next() {
		var rs surveyor.RigState
		if err := rows.Scan(&rs.Name, &rs.Enabled, &rs.Status, &rs.LastSeen); err != nil {
			return nil, err
		}
		result = append(result, rs)
	}
	return result, rows.Err()
}

// readActualAgents fetches the actual_agent_config table.
func readActualAgents(ctx context.Context, db *sql.DB) ([]surveyor.AgentState, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT rig_name, role, status, last_seen FROM actual_agent_config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []surveyor.AgentState
	for rows.Next() {
		var ag surveyor.AgentState
		if err := rows.Scan(&ag.RigName, &ag.Role, &ag.Status, &ag.LastSeen); err != nil {
			return nil, err
		}
		result = append(result, ag)
	}
	return result, rows.Err()
}

// readActualWorktrees fetches the actual_worktrees table.
func readActualWorktrees(ctx context.Context, db *sql.DB) ([]surveyor.WorktreeState, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT rig_name, status, last_seen FROM actual_worktrees`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []surveyor.WorktreeState
	for rows.Next() {
		var ws surveyor.WorktreeState
		if err := rows.Scan(&ws.RigName, &ws.Status, &ws.LastSeen); err != nil {
			return nil, err
		}
		result = append(result, ws)
	}
	return result, rows.Err()
}

// readActualCustomRoles fetches the actual_custom_roles table.
func readActualCustomRoles(ctx context.Context, db *sql.DB) ([]surveyor.CustomRoleState, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT rig_name, role_name, instance_index, status, last_seen
		   FROM actual_custom_roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []surveyor.CustomRoleState
	for rows.Next() {
		var cs surveyor.CustomRoleState
		if err := rows.Scan(&cs.RigName, &cs.RoleName, &cs.InstanceIndex, &cs.Status, &cs.LastSeen); err != nil {
			return nil, err
		}
		result = append(result, cs)
	}
	return result, rows.Err()
}
