// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file provides the coordinator types and functions that combine the
// individual topology SQL generators (topology.go, customroles.go, costpolicy.go)
// into the full apply transaction statement list used by apply.go.
package townctl

import (
	"fmt"
	"strings"

	"github.com/tenev/dgt/pkg/manifest"
)

// TopologyOp is a single planned operation for the dry-run output.
type TopologyOp struct {
	Action string // "add", "update", or "remove"
	Table  string
	Key    string // human-readable primary key description
	Detail string // field summary for updates
}

// FullApplySQL generates the complete ordered SQL statement list for an atomic
// Dolt apply transaction covering all desired_topology tables:
//
//   - desired_rigs, desired_agent_config, desired_formulas (topology.go)
//   - desired_custom_roles, desired_rig_custom_roles (customroles.go)
//   - desired_cost_policy (costpolicy.go)
//
// The individual generators each prepend their own desired_topology_versions
// upsert. Callers wrap this list in BEGIN / COMMIT.
func FullApplySQL(m *manifest.TownManifest) []Stmt {
	var stmts []Stmt
	stmts = append(stmts, TopologyApplySQL(m)...)
	stmts = append(stmts, CustomRolesApplySQL(m)...)
	stmts = append(stmts, ApplySQL(m)...)
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
