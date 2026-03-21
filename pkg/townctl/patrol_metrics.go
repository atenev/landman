// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file registers Prometheus metrics for the Deacon cost patrol (dgt-bp9).
// Metrics are registered on the default prometheus.DefaultRegisterer so they
// are automatically exported by any HTTP handler using promhttp.Handler().
package townctl

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// patrolRunsTotal counts completed patrol evaluations per rig.
	patrolRunsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "townctl",
			Name:      "patrol_runs_total",
			Help:      "Total number of completed cost patrol evaluations by rig.",
		},
		[]string{"rig"},
	)

	// patrolActionsTotal counts warn and drain actions issued per rig.
	patrolActionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "townctl",
			Name:      "patrol_actions_total",
			Help:      "Total number of patrol actions (warn or drain) issued by action type and rig.",
		},
		[]string{"action", "rig"},
	)

	// patrolBudgetPctUsed tracks the current budget consumption percentage per
	// rig and budget type. Updated at the end of every patrol run.
	patrolBudgetPctUsed = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "townctl",
			Name:      "patrol_budget_pct_used",
			Help:      "Current percentage of daily budget consumed by rig and budget type (usd/messages/tokens).",
		},
		[]string{"rig", "budget_type"},
	)

	// ledgerWriteDurationSeconds measures the wall-clock time of each cost
	// ledger write. Callers record this via ObserveLedgerWrite.
	ledgerWriteDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: "townctl",
			Name:      "ledger_write_duration_seconds",
			Help:      "Wall-clock time of each cost ledger write to Dolt.",
			Buckets:   prometheus.DefBuckets,
		},
	)
)

// recordPatrolRun updates patrol metrics from one completed patrol evaluation.
// It increments patrolRunsTotal for every row evaluated, sets the
// patrolBudgetPctUsed gauge for each (rig, budget_type), and increments
// patrolActionsTotal for each Bead that resulted in a warn or drain action.
func recordPatrolRun(rows []PatrolRow, beads []PatrolBead) {
	for _, row := range rows {
		patrolRunsTotal.WithLabelValues(row.RigName).Inc()
		patrolBudgetPctUsed.WithLabelValues(row.RigName, row.BudgetType).Set(row.PctUsed())
	}
	for _, bead := range beads {
		if bead.Action == PatrolWarn || bead.Action == PatrolDrain {
			patrolActionsTotal.WithLabelValues(bead.Action.String(), bead.RigName).Inc()
		}
	}
}

// ObserveLedgerWrite records the duration of a cost ledger write to Dolt.
// Call this after each ExecTransaction that persists cost ledger rows, passing
// the elapsed duration measured around the write.
func ObserveLedgerWrite(d time.Duration) {
	ledgerWriteDurationSeconds.Observe(d.Seconds())
}
