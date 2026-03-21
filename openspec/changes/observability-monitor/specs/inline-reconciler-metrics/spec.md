# Spec: Inline Reconciler Metrics

**Architecture decision**: [ADR-0011](../../../../docs/adr/0011-observability-architecture.md) (D1: inline pkg/surveyor metrics are fine, D4: cardinality constraints)

## Purpose

Add Prometheus instrumentation directly to the Go libraries used by the Surveyor
reconciler (`pkg/surveyor`) and cost patrol (`pkg/townctl`). These are event-driven
metrics that fire as the reconcile state machine transitions, giving precise timing
and outcome data that cannot be derived from Dolt polling alone.

## pkg/surveyor Metrics

Added in `pkg/surveyor/metrics.go`. Registration via `RegisterMetrics(reg)` using
`sync.Once` to be safe when used as a library (multiple test instances).

### surveyor_reconcile_total

- **Type**: Counter
- **Labels**: `outcome` (success / escalated / abandoned)
- **Callsite**: End of each reconcile run, after outcome is determined.
- **success**: Verify loop passed, reconcile branch merged.
- **escalated**: Surveyor filed escalation Bead to Mayor.
- **abandoned**: Reconcile abandoned (e.g., concurrent reconcile guard blocked it).

### surveyor_convergence_score

- **Type**: Gauge
- **Labels**: none
- **Value**: `ScoreResult.Score` from the most recent `ComputeScore()` call during verify.
- **Callsite**: After each `ComputeScore()` call in the verify loop.
- **Semantics**: Reflects the last score seen by the Surveyor in the verify loop —
  distinct from the observer's continuous poll. The Surveyor's gauge reflects in-reconcile
  score; the observer's gauge reflects current Dolt state (which may differ if no reconcile
  is in progress). Both are useful. Dashboard should display both.

### surveyor_verify_retries

- **Type**: Histogram
- **Labels**: none
- **Buckets**: [1, 2, 3, 4, 5, 7, 10, +Inf]
- **Value**: `VerifyOutcome.Attempts` — number of verify loop iterations for this reconcile.
- **Callsite**: After verify loop completes (success or escalation).
- **Semantics**: p50 = 1 (most reconciles converge first try). p99 > 7 = consistently
  slow convergence.

### surveyor_escalations_total

- **Type**: Counter
- **Labels**: `reason` (verify-exhausted / score-regression / dog-failure)
- **Callsite**: When `VerifyOutcome.Escalation != ""`, increment with `VerifyOutcome.Escalation`.
- **Semantics**: Rising `score-regression` indicates fleet actively diverging.
  Rising `dog-failure` indicates Dogs are failing to execute operations.

### surveyor_reconcile_duration_seconds

- **Type**: Histogram
- **Labels**: `outcome` (success / escalated / abandoned)
- **Buckets**: [1, 5, 10, 30, 60, 120, 300, 600, 1800, +Inf] (seconds)
- **Value**: Wall time from reconcile start to outcome determination.
- **Callsite**: `defer observeReconcile(start, &outcome)` pattern (matches operator pattern).

### surveyor_stale_branch_cleanups_total

- **Type**: Counter
- **Labels**: none
- **Value**: Count of stale `reconcile/*` branches cleaned up on Surveyor startup.
- **Callsite**: Stale branch cleanup loop in GUPP startup protocol.
- **Semantics**: Non-zero value after startup = Surveyor crashed mid-reconcile.
  Persistent non-zero across restarts = Surveyor crashes repeatedly.

## pkg/townctl Metrics (Cost Patrol)

Added in `pkg/townctl/patrol_metrics.go`. Called from `patrol.go`.

### townctl_patrol_runs_total

- **Type**: Counter
- **Labels**: `rig`
- **Callsite**: Once per rig at the end of each patrol run (regardless of action taken).
- **Semantics**: Rate ≈ `1 / patrol_interval_seconds`. Drop in rate = patrol not running.

### townctl_patrol_actions_total

- **Type**: Counter
- **Labels**: `action` (warn / drain), `rig`
- **Callsite**: When `PatrolAction != PatrolNone`, before filing the Bead.
- **Semantics**: Rising `drain` for the same rig = budget chronically exceeded.

### townctl_patrol_budget_pct_used

- **Type**: Gauge
- **Labels**: `rig`, `budget_type` (usd / messages / tokens)
- **Value**: `PatrolRow.Spend / PatrolRow.DailyBudget * 100` at time of patrol.
- **Callsite**: Updated for every rig on every patrol run.
- **Semantics**: Continuous visibility into budget trajectory, not just when thresholds
  are crossed. Alert at 80% (warn threshold - 10%) for proactive intervention.

### townctl_ledger_write_duration_seconds

- **Type**: Histogram
- **Labels**: none
- **Buckets**: [0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, +Inf]
- **Callsite**: Around the Dolt transaction that writes cost ledger entries.
- **Semantics**: p99 > 100ms indicates Dolt write contention.

## Registration Pattern

```go
// pkg/surveyor/metrics.go

var (
    surveyorReconcileTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "surveyor_reconcile_total",
            Help: "Reconcile attempts by outcome.",
        },
        []string{"outcome"},
    )
    // ... other collectors
)

var registerOnce sync.Once

// RegisterMetrics registers surveyor metrics with reg.
// Safe to call multiple times.
func RegisterMetrics(reg prometheus.Registerer) {
    registerOnce.Do(func() {
        reg.MustRegister(surveyorReconcileTotal, /* ... */)
    })
}
```

Tests call `RegisterMetrics(prometheus.NewRegistry())` once in `TestMain`.

## Non-Goals

- These metrics are NOT added to the Surveyor CLAUDE.md or LLM context.
- The Surveyor Go process embeds these metrics; they are served on the Surveyor's own
  Prometheus endpoint (`:9092` by default, separate from operator `:8080` and
  observer `:9091`). The endpoint is started by the Surveyor Go wrapper, not the
  LLM agent.
