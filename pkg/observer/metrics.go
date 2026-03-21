// Package observer provides the Prometheus metrics and data-collection layer
// for the dgt-observer process (ADR-0011, ADR-0012).
//
// This file defines and registers all dgt-observer Prometheus collectors per
// the catalogue in ADR-0011. Callers obtain a *Metrics by calling
// RegisterMetrics(reg) and record observations through its fields.
package observer

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tenev/dgt/pkg/surveyor"
)

// Metrics holds all Prometheus collectors defined by pkg/observer.
// Every field is set to a live registered collector after RegisterMetrics
// returns; callers must not read or write fields before calling RegisterMetrics.
type Metrics struct {
	// FleetConvergenceScore is the fraction of desired topology rows matching
	// actual state, per rig. Range [0.0, 1.0].
	FleetConvergenceScore *prometheus.GaugeVec

	// AgentStalenessSeconds is the seconds elapsed since the last heartbeat
	// for each running agent pool member, by rig and role.
	AgentStalenessSeconds *prometheus.GaugeVec

	// PoolSizeDesired is the desired number of agent pool slots per rig.
	PoolSizeDesired *prometheus.GaugeVec

	// PoolSizeActual is the actual number of running agent pool members per rig.
	PoolSizeActual *prometheus.GaugeVec

	// PoolSizeDelta is the difference between desired and actual pool size
	// per rig. Positive values mean the pool is under-provisioned.
	PoolSizeDelta *prometheus.GaugeVec

	// WorktreesTotal is the number of git worktrees per rig, labelled by
	// their lifecycle status (e.g. "active", "idle", "stale").
	WorktreesTotal *prometheus.GaugeVec

	// BeadsOpenTotal is the number of currently open Beads issues, labelled
	// by issue type and priority bucket.
	BeadsOpenTotal *prometheus.GaugeVec

	// BeadsFiledTotal is the cumulative count of Beads issues filed since the
	// observer process started.
	BeadsFiledTotal prometheus.Counter

	// BeadsClosedTotal is the cumulative count of Beads issues closed since
	// the observer process started.
	BeadsClosedTotal prometheus.Counter

	// BeadsLatencySeconds is the distribution of time from filing to closing
	// a Beads issue, labelled by issue type.
	BeadsLatencySeconds *prometheus.HistogramVec

	// DoltPollDurationSeconds is the distribution of wall-clock time for a
	// single Dolt topology poll cycle.
	DoltPollDurationSeconds prometheus.Histogram

	// DoltPollErrorsTotal is the cumulative count of failed Dolt topology poll
	// cycles since the observer process started.
	DoltPollErrorsTotal prometheus.Counter
}

// newMetrics allocates a set of Prometheus collectors without registering them.
func newMetrics() *Metrics {
	return &Metrics{
		FleetConvergenceScore: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "fleet_convergence_score",
				Help:      "Fraction of desired topology rows matching actual state (0.0–1.0), by rig.",
			},
			[]string{"rig"},
		),
		AgentStalenessSeconds: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "agent_staleness_seconds",
				Help:      "Seconds since the last heartbeat for an agent pool member, by rig and role.",
			},
			[]string{"rig", "role"},
		),
		PoolSizeDesired: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "pool_size_desired",
				Help:      "Desired number of agent pool slots per rig.",
			},
			[]string{"rig"},
		),
		PoolSizeActual: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "pool_size_actual",
				Help:      "Actual number of running agent pool members per rig.",
			},
			[]string{"rig"},
		),
		PoolSizeDelta: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "pool_size_delta",
				Help:      "Difference between desired and actual pool size per rig (positive = under-provisioned).",
			},
			[]string{"rig"},
		),
		WorktreesTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "worktrees_total",
				Help:      "Number of git worktrees per rig, by lifecycle status.",
			},
			[]string{"rig", "status"},
		),
		BeadsOpenTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "dgt",
				Name:      "beads_open_total",
				Help:      "Number of currently open Beads issues, by type and priority.",
			},
			[]string{"type", "priority"},
		),
		BeadsFiledTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: "dgt",
				Name:      "beads_filed_total",
				Help:      "Cumulative number of Beads issues filed since observer start.",
			},
		),
		BeadsClosedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: "dgt",
				Name:      "beads_closed_total",
				Help:      "Cumulative number of Beads issues closed since observer start.",
			},
		),
		BeadsLatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "dgt",
				Name:      "beads_latency_seconds",
				Help:      "Time from filing to closing a Beads issue, by type.",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"type"},
		),
		DoltPollDurationSeconds: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: "dgt",
				Name:      "dolt_poll_duration_seconds",
				Help:      "Wall-clock time of a single Dolt topology poll cycle.",
				Buckets:   prometheus.DefBuckets,
			},
		),
		DoltPollErrorsTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: "dgt",
				Name:      "dolt_poll_errors_total",
				Help:      "Cumulative number of failed Dolt topology poll cycles since observer start.",
			},
		),
	}
}

// RegisterMetrics registers all dgt-observer Prometheus collectors with reg
// and returns a *Metrics whose fields refer to the live registered collectors.
//
// It is idempotent: if any collector with the same descriptor is already
// registered with reg (e.g. from a prior call), the previously-registered
// instance is used in place of the newly-created one, and no error is returned.
// Any other registration error causes a panic.
func RegisterMetrics(reg prometheus.Registerer) *Metrics {
	m := newMetrics()
	m.FleetConvergenceScore = registerGaugeVec(reg, m.FleetConvergenceScore)
	m.AgentStalenessSeconds = registerGaugeVec(reg, m.AgentStalenessSeconds)
	m.PoolSizeDesired = registerGaugeVec(reg, m.PoolSizeDesired)
	m.PoolSizeActual = registerGaugeVec(reg, m.PoolSizeActual)
	m.PoolSizeDelta = registerGaugeVec(reg, m.PoolSizeDelta)
	m.WorktreesTotal = registerGaugeVec(reg, m.WorktreesTotal)
	m.BeadsOpenTotal = registerGaugeVec(reg, m.BeadsOpenTotal)
	m.BeadsFiledTotal = registerCounter(reg, m.BeadsFiledTotal)
	m.BeadsClosedTotal = registerCounter(reg, m.BeadsClosedTotal)
	m.BeadsLatencySeconds = registerHistogramVec(reg, m.BeadsLatencySeconds)
	m.DoltPollDurationSeconds = registerHistogram(reg, m.DoltPollDurationSeconds)
	m.DoltPollErrorsTotal = registerCounter(reg, m.DoltPollErrorsTotal)
	return m
}

// registerGaugeVec registers c with reg and returns the live collector.
// On AlreadyRegisteredError the existing GaugeVec is returned.
// Any other error causes a panic.
func registerGaugeVec(reg prometheus.Registerer, c *prometheus.GaugeVec) *prometheus.GaugeVec {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector.(*prometheus.GaugeVec)
		}
		panic(err)
	}
	return c
}

// registerHistogramVec registers c with reg and returns the live collector.
// On AlreadyRegisteredError the existing HistogramVec is returned.
// Any other error causes a panic.
func registerHistogramVec(reg prometheus.Registerer, c *prometheus.HistogramVec) *prometheus.HistogramVec {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector.(*prometheus.HistogramVec)
		}
		panic(err)
	}
	return c
}

// registerHistogram registers c with reg and returns the live collector.
// On AlreadyRegisteredError the existing Histogram is returned.
// Any other error causes a panic.
func registerHistogram(reg prometheus.Registerer, c prometheus.Histogram) prometheus.Histogram {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector.(prometheus.Histogram)
		}
		panic(err)
	}
	return c
}

// registerCounter registers c with reg and returns the live collector.
// On AlreadyRegisteredError the existing Counter is returned.
// Any other error causes a panic.
func registerCounter(reg prometheus.Registerer, c prometheus.Counter) prometheus.Counter {
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return are.ExistingCollector.(prometheus.Counter)
		}
		panic(err)
	}
	return c
}

// UpdateMetrics records topology observations into m from snap.
//
// For each agent in snap.Actual.Agents the AgentStalenessSeconds gauge is set
// to the number of seconds elapsed since that agent's last heartbeat. For each
// desired rig in snap.Desired.Rigs the pool size gauges are updated: desired
// reflects MaxPolecats, actual counts active worktrees for that rig, and delta
// is desired minus actual (positive = under-provisioned).
//
// now is passed explicitly so callers can use a fixed clock in tests.
func UpdateMetrics(m *Metrics, snap TopologySnapshot, desired surveyor.DesiredTopology, now time.Time) {
	// Agent staleness.
	for _, ag := range snap.Actual.Agents {
		staleness := now.Sub(ag.LastSeen).Seconds()
		m.AgentStalenessSeconds.WithLabelValues(ag.RigName, ag.Role).Set(staleness)
	}

	// Pool size gauges: count active worktrees per rig.
	activeByRig := make(map[string]int, len(snap.Actual.Worktrees))
	for _, wt := range snap.Actual.Worktrees {
		if wt.Status == "active" {
			activeByRig[wt.RigName]++
		}
	}

	for _, dr := range desired.Rigs {
		d := float64(dr.MaxPolecats)
		a := float64(activeByRig[dr.Name])
		m.PoolSizeDesired.WithLabelValues(dr.Name).Set(d)
		m.PoolSizeActual.WithLabelValues(dr.Name).Set(a)
		m.PoolSizeDelta.WithLabelValues(dr.Name).Set(d - a)
	}
}
