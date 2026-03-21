// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file registers Prometheus metrics for the apply pipeline (dgt-vjp).
// Metrics are registered on the default prometheus.DefaultRegisterer so they
// are automatically exported by any HTTP handler using promhttp.Handler().
package townctl

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// applyDurationSeconds measures the wall-clock time of a complete
	// townctl.Apply() call, labelled by outcome ("ok" or "error").
	applyDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "townctl",
			Name:      "apply_duration_seconds",
			Help:      "Wall-clock time of a complete townctl.Apply() call.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"outcome"},
	)

	// applyErrorsTotal counts the number of Apply() calls that returned an
	// error, labelled by the pipeline step that failed.
	applyErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "townctl",
			Name:      "apply_errors_total",
			Help:      "Total number of failed townctl.Apply() calls by pipeline step.",
		},
		[]string{"step"},
	)

	// topologyDiffOpsTotal counts individual SQL statements written to Dolt
	// across all Apply() transactions, labelled by table and action
	// ("insert", "update", "delete").  This gives a coarse proxy for the
	// number of topology changes applied over time.
	topologyDiffOpsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "townctl",
			Name:      "topology_diff_ops_total",
			Help:      "Total SQL statements executed against Dolt topology tables by table and action.",
		},
		[]string{"table", "action"},
	)
)
