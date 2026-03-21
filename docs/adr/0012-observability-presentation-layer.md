# ADR-0012: Observability Presentation Layer

- **Status**: Proposed
- **Date**: 2026-03-21
- **Depends on**: ADR-0011 (metrics collection architecture)
- **Beads issue**: TBD (see observability-monitor change)

## Context

ADR-0011 established how metrics are collected: a `dgt-observer` binary polls Dolt,
inline metrics fire in `pkg/surveyor` and `pkg/townctl`, all exposed as Prometheus
endpoints. That ADR is intentionally silent on how a human operator actually sees
and acts on that information.

Four presentation surfaces need explicit decisions:

1. **CLI**: how does an operator get fleet health without a running Prometheus stack?
2. **Kubernetes status**: what does `kubectl describe gastown` tell you?
3. **Dashboard**: what panels, layout, and data sources?
4. **Alerting**: which conditions page on-call, and how?

These are separate from collection concerns. A wrong alerting threshold has no impact
on how metrics are stored. A useful `status` command requires no Prometheus at all.

## Goals / Non-Goals

**Goals:**
- Specify `town-ctl status` subcommand: output format, data sources, flags
- Specify CRD status enrichment: which fields, how computed, update frequency
- Specify Grafana dashboard: panel layout, data sources, key queries
- Specify AlertManager `PrometheusRule`: alert names, expressions, thresholds, severities

**Non-Goals:**
- Log aggregation (Loki/ELK)
- Distributed tracing
- Notification routing configuration (that is operator responsibility)
- SLO/error budget tracking

## Decisions

### D1: `town-ctl status` reads Dolt directly, not the observer's /metrics

`town-ctl status` connects to Dolt (same `townctl.ConnectDSN` used by `apply`) and
calls `surveyor.ComputeScore` directly. It does not scrape `/metrics` from the observer.

**Why**: The status command must work in environments where Prometheus is not deployed
(laptop, bare metal without monitoring stack, CI pipeline). The observer's job is
continuous metric export; `town-ctl status` is a point-in-time human query.

**Why not re-use `pkg/observer`**: `pkg/observer/dolt.go` reads the same tables `status`
needs. However, `status` is a simpler read path (no Prometheus collectors, no poll loop)
and will be implemented in `pkg/townctl` directly to keep the dependency boundary clean.
The two can be merged if duplication becomes a problem.

### D2: CRD status is updated by the existing status-sync loop, not the observer

`GasTownReconciler.patchStatusFromActual` already polls Dolt every 30s. It currently
only reads `actual_town.last_reconcile_at`. We extend it to also call `ComputeScore`
and write `ConvergenceScore` and `NonConverged` to `GasTownStatus`.

**Why not the observer**: The observer has no K8s API access (ADR-0011 D5). Adding K8s
access to the observer would couple two concerns. The operator already has the
reconcile loop, K8s credentials, and CRD write access.

**Trade-off**: CRD status lags by up to 30s (status-sync interval). Acceptable — this
is a summary view for `kubectl describe`, not a real-time indicator.

### D3: Grafana dashboard is a versioned JSON file in the chart, not auto-generated

A single `gastown-fleet-health.json` dashboard definition is shipped in `charts/grafana/`
and provisioned via a Kubernetes ConfigMap. It is not generated from code.

**Why not a Grafana operator or grafonnet**: introduces toolchain complexity with no
benefit at current scale. A static JSON file is readable, diffable, and reviewable.

The dashboard is optional — the chart's `grafana.enabled` value defaults to `false`.
Platform teams that already run Grafana can import the JSON manually.

### D4: AlertManager rules are a `PrometheusRule` CRD in the chart

Six alert rules are shipped in `charts/templates/prometheusrule.yaml`. The `PrometheusRule`
CRD is from the Prometheus Operator (kube-prometheus-stack). The chart conditionally
renders this template only when `prometheusRule.enabled=true` (default: false) to avoid
breaking installs without the Prometheus Operator.

**Why six rules, not more**: alert fatigue. The six rules cover the four operational
risk categories (convergence, staleness, budget, backlog). Additional rules are left to
operators who know their own tolerance thresholds.

**Thresholds are values.yaml defaults**: all numeric thresholds (convergence < 0.9,
staleness > 120s, budget > 90%, queue > 10) are exposed as `prometheusRule.*` values
so operators can tune without forking the chart.

### D5: Output format for `town-ctl status` is human-readable by default, JSON with --output=json

Default output is a colored (if TTY) terminal table. `--output=json` emits a single JSON
object with all the same data, for scripting and CI integration.

The JSON schema is stable and versioned (a `version: 1` field). Output to non-TTY stdout
automatically disables color (no `--no-color` flag needed; detect via `os.Stdout.Fd()`
+ `term.IsTerminal`).

## Metric Queries in the Dashboard

Key Prometheus queries for each panel (grounded in ADR-0011 metric catalogue):

| Panel | Query |
|-------|-------|
| Fleet convergence score | `dgt_fleet_convergence_score_total` |
| Converged rig count | `count(dgt_fleet_convergence_score == 1)` |
| Stale agent count | `count(dgt_agent_staleness_seconds > 60)` |
| Per-rig score timeline | `dgt_fleet_convergence_score{rig=~"$rig"}` |
| Beads queue depth | `dgt_beads_open_total` |
| Beads latency p99 | `histogram_quantile(0.99, rate(dgt_beads_latency_seconds_bucket[5m]))` |
| Budget % by rig | `townctl_patrol_budget_pct_used` |
| Reconcile rate | `rate(surveyor_reconcile_total[5m])` |
| Escalation rate | `rate(surveyor_escalations_total[5m])` |
| Surveyor verify p99 | `histogram_quantile(0.99, rate(surveyor_verify_retries_bucket[10m]))` |

## Risks / Trade-offs

- **`town-ctl status` and `pkg/observer/dolt.go` duplicate Dolt read logic**: acceptable
  short-term. If they diverge, extract a shared `pkg/doltreader` package. Deferred.
- **Grafana dashboard JSON drifts from metric names**: metric names are stable after
  ADR-0011 is accepted. Breaking changes to metric names require a dashboard update.
  Mitigated by shipping both together in the same chart.
- **PrometheusRule CRD not installed**: alerting silently does nothing if Prometheus
  Operator is absent. Mitigated by `prometheusRule.enabled=false` default and clear
  chart docs.
- **CRD status 30s lag**: `kubectl describe gastown` may show a stale score. Acceptable
  — the Grafana dashboard is the real-time view. Document the lag in the status field
  description.
