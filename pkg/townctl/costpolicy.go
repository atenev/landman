// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements cost policy resolution (dgt-2xf): the inheritance chain
// that maps [rig.cost] / [defaults.cost] blocks in town.toml to rows in the
// desired_cost_policy Dolt table, and the SQL statements that keep that table
// in sync during a town-ctl apply transaction.
package townctl

import (
	"fmt"
	"strings"

	"github.com/tenev/dgt/pkg/manifest"
)

const (
	// defaultWarnAtPct is applied when a cost block is present but warn_at_pct
	// is not set. Matches the Dolt column default (ADR-0006).
	defaultWarnAtPct = 80

	// costPolicySchemaVersion is the schema version written to
	// desired_topology_versions (ADR-0003 contract).
	costPolicySchemaVersion = 1
)

// CostPolicyRow represents one row in the desired_cost_policy Dolt table.
type CostPolicyRow struct {
	RigName     string
	BudgetType  string  // "usd", "messages", or "tokens"
	DailyBudget float64 // DECIMAL(16,4); unit depends on BudgetType
	WarnAtPct   int     // [1, 99]
}

// ResolveCostPolicies applies the inheritance chain defined in ADR-0006 and
// returns the set of desired_cost_policy rows for m.
//
// Inheritance: [rig.cost] > [defaults.cost] > unrestricted.
// Rigs that resolve to unrestricted produce NO row (absence = unrestricted).
func ResolveCostPolicies(m *manifest.TownManifest) []CostPolicyRow {
	rows := make([]CostPolicyRow, 0, len(m.Rigs))
	for _, rig := range m.Rigs {
		if row, ok := resolveRigPolicy(rig, m.Defaults); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func resolveRigPolicy(rig manifest.RigSpec, defaults manifest.RigDefaults) (CostPolicyRow, bool) {
	if !rig.Cost.IsEmpty() {
		return costPolicyToRow(rig.Name, rig.Cost), true
	}
	if !defaults.Cost.IsEmpty() {
		return costPolicyToRow(rig.Name, defaults.Cost), true
	}
	return CostPolicyRow{}, false // unrestricted
}

func costPolicyToRow(rigName string, p manifest.CostPolicy) CostPolicyRow {
	row := CostPolicyRow{RigName: rigName, WarnAtPct: defaultWarnAtPct}
	if p.WarnAtPct != nil {
		row.WarnAtPct = *p.WarnAtPct
	}
	switch {
	case p.DailyBudgetUSD != nil:
		row.BudgetType = "usd"
		row.DailyBudget = *p.DailyBudgetUSD
	case p.DailyBudgetMessages != nil:
		row.BudgetType = "messages"
		row.DailyBudget = float64(*p.DailyBudgetMessages)
	case p.DailyBudgetTokens != nil:
		row.BudgetType = "tokens"
		row.DailyBudget = float64(*p.DailyBudgetTokens)
	}
	return row
}

// ApplySQL returns the ordered SQL statements that must be executed inside the
// atomic Dolt apply transaction to bring desired_cost_policy in sync with m.
//
// Statement order (ADR-0003 contract):
//  1. desired_topology_versions upsert  — MUST be first
//  2. UPSERT one row per rig with an active cost policy
//  3. DELETE rows for rigs no longer in the desired set
//
// The caller wraps these in BEGIN / COMMIT and executes them against Dolt.
func ApplySQL(m *manifest.TownManifest) []Stmt {
	desired := ResolveCostPolicies(m)
	stmts := make([]Stmt, 0, 2+len(desired))

	// 1. ADR-0003: versions row first in every transaction touching this table.
	stmts = append(stmts, TopologyVersionsUpsert([]TableSchemaVersion{
		{Table: "desired_cost_policy", Version: costPolicySchemaVersion},
	}))

	// 2. Upsert the desired rows.
	for _, row := range desired {
		stmts = append(stmts, upsertRow(row))
	}

	// 3. Remove rows for rigs no longer in the desired set (removed rigs and
	//    rigs that have become unrestricted both fall into this category).
	stmts = append(stmts, cleanupRows(desired))

	return stmts
}

func upsertRow(r CostPolicyRow) Stmt {
	return Stmt{
		Query: "INSERT INTO desired_cost_policy (rig_name, budget_type, daily_budget, warn_at_pct)" +
			" VALUES (?, ?, ?, ?)" +
			" ON DUPLICATE KEY UPDATE budget_type = VALUES(budget_type)," +
			" daily_budget = VALUES(daily_budget), warn_at_pct = VALUES(warn_at_pct);",
		Args: []any{r.RigName, r.BudgetType, r.DailyBudget, r.WarnAtPct},
	}
}

// cleanupRows returns a Stmt that removes any desired_cost_policy rows not
// present in the desired set. When desired is empty (all rigs are unrestricted)
// all rows are deleted.
func cleanupRows(desired []CostPolicyRow) Stmt {
	if len(desired) == 0 {
		return Stmt{Query: "DELETE FROM desired_cost_policy;"}
	}
	placeholders := strings.Repeat("?, ", len(desired))
	placeholders = placeholders[:len(placeholders)-2]
	args := make([]any, len(desired))
	for i, row := range desired {
		args[i] = row.RigName
	}
	return Stmt{
		Query: fmt.Sprintf(
			"DELETE FROM desired_cost_policy WHERE rig_name NOT IN (%s);",
			placeholders),
		Args: args,
	}
}

// DiffOp describes a single planned operation on desired_cost_policy.
type DiffOp struct {
	// Action is "add", "update", or "remove".
	Action string
	Row    CostPolicyRow
}

// DryRunPlan computes the diff between the desired policy set (derived from m)
// and the rows currently in desired_cost_policy (current). It returns the
// planned operations that ApplySQL would perform, skipping no-op rows.
//
// current is the slice of rows read from Dolt before the apply.
func DryRunPlan(m *manifest.TownManifest, current []CostPolicyRow) []DiffOp {
	desired := ResolveCostPolicies(m)

	currentByName := make(map[string]CostPolicyRow, len(current))
	for _, row := range current {
		currentByName[row.RigName] = row
	}

	var plan []DiffOp

	desiredNames := make(map[string]struct{}, len(desired))
	for _, row := range desired {
		desiredNames[row.RigName] = struct{}{}
		if cur, exists := currentByName[row.RigName]; !exists {
			plan = append(plan, DiffOp{Action: "add", Row: row})
		} else if cur != row {
			plan = append(plan, DiffOp{Action: "update", Row: row})
		}
	}

	for _, cur := range current {
		if _, keep := desiredNames[cur.RigName]; !keep {
			plan = append(plan, DiffOp{Action: "remove", Row: cur})
		}
	}
	return plan
}

// FormatDryRun renders a DryRunPlan as human-readable text for stdout.
// Uses +/~/- prefixes matching the town-ctl dry-run convention (ADR-0001).
func FormatDryRun(plan []DiffOp) string {
	if len(plan) == 0 {
		return "desired_cost_policy: no changes\n"
	}
	var b strings.Builder
	for _, op := range plan {
		r := op.Row
		switch op.Action {
		case "add":
			fmt.Fprintf(&b, "+ desired_cost_policy: rig_name=%s budget_type=%s daily_budget=%.4f warn_at_pct=%d\n",
				r.RigName, r.BudgetType, r.DailyBudget, r.WarnAtPct)
		case "update":
			fmt.Fprintf(&b, "~ desired_cost_policy: rig_name=%s budget_type=%s daily_budget=%.4f warn_at_pct=%d\n",
				r.RigName, r.BudgetType, r.DailyBudget, r.WarnAtPct)
		case "remove":
			fmt.Fprintf(&b, "- desired_cost_policy: rig_name=%s\n", r.RigName)
		}
	}
	return b.String()
}
