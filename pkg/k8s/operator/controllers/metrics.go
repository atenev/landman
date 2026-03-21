// Package controllers implements controller-runtime reconcilers for all four
// Gas Town CRDs: GasTown, Rig, AgentRole, and DoltInstance.
//
// This file registers Prometheus metrics for the operator controllers (dgt-vjp).
// controller-runtime automatically registers default Go runtime and workqueue
// metrics; this file adds Gas Town business-level metrics.
package controllers

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// reconcileDurationSeconds measures the wall-clock time of each
	// reconcile loop iteration, labelled by controller and outcome.
	reconcileDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "gastown_operator",
			Name:      "reconcile_duration_seconds",
			Help:      "Wall-clock time of a single reconcile loop iteration by controller.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"controller", "outcome"},
	)

	// reconcileErrorsTotal counts reconcile iterations that returned an error,
	// labelled by controller.
	reconcileErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gastown_operator",
			Name:      "reconcile_errors_total",
			Help:      "Total number of failed reconcile iterations by controller.",
		},
		[]string{"controller"},
	)
)

// observeReconcile records reconcile duration and error metrics for controller.
// It is intended to be called via defer at the start of each Reconcile method,
// using a named return error pointer so the deferred call sees the final value:
//
//	func (r *FooReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, retErr error) {
//	    defer observeReconcile("foo", time.Now(), &retErr)
//	    ...
//	}
func observeReconcile(controller string, start time.Time, retErr *error) {
	outcome := "ok"
	if retErr != nil && *retErr != nil {
		outcome = "error"
		reconcileErrorsTotal.WithLabelValues(controller).Inc()
	}
	reconcileDurationSeconds.WithLabelValues(controller, outcome).Observe(time.Since(start).Seconds())
}
