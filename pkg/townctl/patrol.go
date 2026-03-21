package townctl

import (
	"fmt"
	"strings"
)

// PatrolRow is one row returned by the Deacon cost patrol query.
// It represents a rig's current spend vs its daily budget.
// Produced by joining desired_cost_policy LEFT JOIN cost_ledger_24h.
type PatrolRow struct {
	// RigName is the rig identifier.
	RigName string
	// BudgetType is "usd", "messages", or "tokens".
	BudgetType string
	// DailyBudget is the configured cap (same unit as BudgetType).
	DailyBudget float64
	// WarnAtPct is the soft-warning threshold (1–99).
	WarnAtPct int
	// Spend is the rolling 24-hour spend in the same unit as DailyBudget.
	// NULLs from the LEFT JOIN are normalised to 0 before reaching this field.
	Spend float64
}

// PctUsed computes the percentage of the daily budget consumed (0–∞).
func (r PatrolRow) PctUsed() float64 {
	if r.DailyBudget == 0 {
		return 0
	}
	return (r.Spend / r.DailyBudget) * 100
}

// PatrolAction is the action Deacon should take for one rig.
type PatrolAction int

// String implements fmt.Stringer for readable log output.
func (a PatrolAction) String() string {
	switch a {
	case PatrolNone:
		return "none"
	case PatrolWarn:
		return "warn"
	case PatrolDrain:
		return "drain"
	default:
		return fmt.Sprintf("PatrolAction(%d)", int(a))
	}
}

const (
	// PatrolNone means the rig is within budget — no Bead filed.
	PatrolNone PatrolAction = iota
	// PatrolWarn means pct_used >= warn_at_pct but < 100 — file a warning Bead.
	PatrolWarn
	// PatrolDrain means pct_used >= 100 — file a hard-cap drain Bead.
	PatrolDrain
)

// PatrolBead describes a Bead that Deacon should file.
type PatrolBead struct {
	// Action is PatrolWarn or PatrolDrain.
	Action PatrolAction
	// RigName is the subject rig.
	RigName string
	// Title is the Bead title filed to the Beads tracker.
	Title string
	// Description is the Bead body with spend details.
	Description string
	// Priority is 0 for drain (critical), 1 for warn (high).
	Priority int
	// Tag is "cost-cap" for drain or "cost-warning" for warn.
	Tag string
	// AssignTo is the role to assign the Bead to. "mayor" for warnings.
	// Drain Beads are directed at the drain execution path (no specific assignee).
	AssignTo string
}

// ComputePatrolBeads evaluates each PatrolRow and returns the Beads that Deacon
// should file. Rows where pct_used < warn_at_pct produce no Bead.
//
// Decision rules (from spec/deacon-cost-patrol):
//   - pct_used >= 100  → PatrolDrain  (priority 0, tag "cost-cap")
//   - pct_used >= warn_at_pct && < 100 → PatrolWarn (priority 1, tag "cost-warning", assign to "mayor")
//   - pct_used < warn_at_pct → PatrolNone (no Bead)
func ComputePatrolBeads(rows []PatrolRow) []PatrolBead {
	beads := make([]PatrolBead, 0, len(rows))
	for _, row := range rows {
		pct := row.PctUsed()
		switch {
		case pct >= 100:
			beads = append(beads, drainBead(row, pct))
		case pct >= float64(row.WarnAtPct):
			beads = append(beads, warnBead(row, pct))
		}
	}
	recordPatrolRun(rows, beads)
	return beads
}

func drainBead(row PatrolRow, pctUsed float64) PatrolBead {
	return PatrolBead{
		Action:   PatrolDrain,
		RigName:  row.RigName,
		Title:    fmt.Sprintf("COST CAP: drain rig %s", row.RigName),
		Description: fmt.Sprintf(
			"Drain all Polecats on rig %s. Block until Polecat count reaches 0. "+
				"Reason: cost hard cap. "+
				"Spend: %.4f %s / %.4f (%.1f%%). Tag: cost-cap.",
			row.RigName, row.Spend, row.BudgetType, row.DailyBudget, pctUsed,
		),
		Priority: 0,
		Tag:      "cost-cap",
		AssignTo: "",
	}
}

func warnBead(row PatrolRow, pctUsed float64) PatrolBead {
	return PatrolBead{
		Action:  PatrolWarn,
		RigName: row.RigName,
		Title:   fmt.Sprintf("COST WARNING: rig %s at %.0f%%", row.RigName, pctUsed),
		Description: fmt.Sprintf(
			"Rig %s has consumed %.1f%% of its daily %s budget "+
				"(%.4f / %.4f). "+
				"Warn threshold: %d%%.",
			row.RigName, pctUsed, row.BudgetType,
			row.Spend, row.DailyBudget, row.WarnAtPct,
		),
		Priority: 1,
		Tag:      "cost-warning",
		AssignTo: "mayor",
	}
}

// PatrolQuery returns the SQL query Deacon runs to compute pct_used per rig.
// The query joins desired_cost_policy against cost_ledger_24h (rolling 24h view).
// NULL spend values from the LEFT JOIN are treated as 0 by COALESCE.
// Rigs absent from desired_cost_policy are excluded entirely (unrestricted rigs).
func PatrolQuery() string {
	return strings.TrimSpace(`
SELECT
    p.rig_name,
    p.budget_type,
    p.daily_budget,
    p.warn_at_pct,
    CASE p.budget_type
        WHEN 'usd'      THEN COALESCE(l.spend_usd, 0)
        WHEN 'messages' THEN COALESCE(l.spend_messages, 0)
        WHEN 'tokens'   THEN COALESCE(l.spend_tokens, 0)
    END AS spend,
    CASE p.budget_type
        WHEN 'usd'      THEN COALESCE(l.spend_usd, 0) / p.daily_budget * 100
        WHEN 'messages' THEN COALESCE(l.spend_messages, 0) / p.daily_budget * 100
        WHEN 'tokens'   THEN COALESCE(l.spend_tokens, 0) / p.daily_budget * 100
    END AS pct_used
FROM desired_cost_policy p
LEFT JOIN cost_ledger_24h l ON l.rig_name = p.rig_name`)
}
