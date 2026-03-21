// Package surveyor - metrics.go
//
// Prometheus metrics for the Surveyor reconciler (ADR-0011).
// Registration is idempotent via sync.Once so the package is safe for use as
// a library that may be imported by multiple callers or test suites.
package surveyor

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds all Prometheus collectors for the Surveyor reconciler.
// Obtain the package-level instance by calling RegisterMetrics; do not
// construct this struct directly.
type Metrics struct {
	// ReconcileTotal counts completed reconcile cycles, labelled by outcome.
	// outcome values: "success" | "escalated" | "abandoned"
	ReconcileTotal *prometheus.CounterVec

	// ConvergenceScore is the most recent convergence score (0.0–1.0).
	ConvergenceScore prometheus.Gauge

	// VerifyRetries is the distribution of verify attempt counts per reconcile.
	VerifyRetries prometheus.Histogram

	// EscalationsTotal counts escalation events, labelled by reason.
	// reason values: "verify-exhausted" | "score-regression" | "dog-failure"
	EscalationsTotal *prometheus.CounterVec

	// ReconcileDurationSeconds is the distribution of reconcile wall-clock
	// durations, labelled by outcome.
	// outcome values: "success" | "escalated" | "abandoned"
	ReconcileDurationSeconds *prometheus.HistogramVec

	// StaleBranchCleanupsTotal counts stale branch cleanups.
	StaleBranchCleanupsTotal prometheus.Counter
}

var (
	pkgMetrics     *Metrics
	pkgMetricsOnce sync.Once
)

// RegisterMetrics registers the Surveyor Prometheus collectors with reg and
// returns the package-level *Metrics instance. Registration happens exactly
// once regardless of how many times this function is called (sync.Once). The
// first caller's reg argument is used; subsequent callers receive the same
// *Metrics without re-registering.
func RegisterMetrics(reg prometheus.Registerer) *Metrics {
	pkgMetricsOnce.Do(func() {
		m := &Metrics{
			ReconcileTotal: prometheus.NewCounterVec(
				prometheus.CounterOpts{
					Namespace: "surveyor",
					Name:      "reconcile_total",
					Help:      "Cumulative number of reconcile cycles, by outcome (success/escalated/abandoned).",
				},
				[]string{"outcome"},
			),
			ConvergenceScore: prometheus.NewGauge(prometheus.GaugeOpts{
				Namespace: "surveyor",
				Name:      "convergence_score",
				Help:      "Most recent convergence score computed by the Surveyor (0.0–1.0).",
			}),
			VerifyRetries: prometheus.NewHistogram(prometheus.HistogramOpts{
				Namespace: "surveyor",
				Name:      "verify_retries",
				Help:      "Distribution of verify attempt counts per reconcile cycle.",
				Buckets:   []float64{1, 2, 3, 5, 7, 10},
			}),
			EscalationsTotal: prometheus.NewCounterVec(
				prometheus.CounterOpts{
					Namespace: "surveyor",
					Name:      "escalations_total",
					Help:      "Cumulative number of escalations, by reason (verify-exhausted/score-regression/dog-failure).",
				},
				[]string{"reason"},
			),
			ReconcileDurationSeconds: prometheus.NewHistogramVec(
				prometheus.HistogramOpts{
					Namespace: "surveyor",
					Name:      "reconcile_duration_seconds",
					Help:      "Distribution of reconcile cycle wall-clock durations, by outcome.",
					Buckets:   prometheus.DefBuckets,
				},
				[]string{"outcome"},
			),
			StaleBranchCleanupsTotal: prometheus.NewCounter(prometheus.CounterOpts{
				Namespace: "surveyor",
				Name:      "stale_branch_cleanups_total",
				Help:      "Cumulative number of stale branch cleanups performed by the Surveyor.",
			}),
		}
		reg.MustRegister(
			m.ReconcileTotal,
			m.ConvergenceScore,
			m.VerifyRetries,
			m.EscalationsTotal,
			m.ReconcileDurationSeconds,
			m.StaleBranchCleanupsTotal,
		)
		pkgMetrics = m
	})
	return pkgMetrics
}
