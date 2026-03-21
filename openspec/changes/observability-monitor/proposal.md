## Why

The dgt control plane converges Gas Town topology through a multi-layer pipeline:
`town-ctl` / `gastown-operator` → Dolt desired_topology → Surveyor (AI reconciler) →
Dogs/Deacon (executor agents) → actual_topology. Every layer works — but none of them
are observable.

The Surveyor is the most critical component: it is the only entity that knows whether
the fleet is converging, diverging, or stuck. Yet its internal state (reconcile attempts,
convergence scores, verify retries, escalation reasons) is completely invisible. The only
signal that something went wrong is a high-priority Bead filed to Mayor — after the fact.

Similarly: `actual_topology` in Dolt captures agent fleet health (staleness, pool counts,
worktree state) but nothing reads those tables to produce metrics. Cost patrol effectiveness
is only visible after a budget is exceeded. Beads workflow timing (filed → executing →
closed) is unknown, so slow Dog execution is undetectable until convergence fails.

The result: a system that is correct in design, blind in operation.

## What Changes

**Collection layer** (ADR-0011):
- New `cmd/dgt-observer` binary: a standalone Go service (not an agent) that runs
  alongside the operator or `town-ctl`. Polls Dolt at a configurable interval, reads
  `actual_topology` + `desired_topology` + Beads state, and exposes a Prometheus `/metrics`
  endpoint. This is the parallel monitoring tool.
- New `pkg/observer` package: Dolt topology reader, Beads workflow reader, and all metric
  collector definitions.
- Inline metrics additions to `pkg/surveyor`: reconcile state machine counters, convergence
  score gauge, verify retry histogram, escalation reason counters.
- Inline metrics additions to `pkg/townctl`: cost patrol run counter, action counters
  (warn/drain) by rig, budget type, ledger write latency.

**Presentation layer** (ADR-0012):
- New `town-ctl status` subcommand: point-in-time fleet health summary rendered as a
  terminal table or JSON. Reads Dolt directly — no Prometheus required.
- CRD status enrichment: `ConvergenceScore`, `NonConverged`, and `LastConvergenceAt`
  added to `GasTownStatus`. Updated by the existing 30s status-sync loop.
- Grafana dashboard: 5-row dashboard JSON in `charts/grafana/`, provisioned via ConfigMap.
- AlertManager `PrometheusRule`: 6 alert rules in `charts/templates/prometheusrule.yaml`,
  all thresholds tunable via `values.yaml`.

## Capabilities

### New Capabilities

- `dgt-observer`: The observer binary — CLI flags (`--dolt-dsn`, `--interval`,
  `--metrics-addr`), polling loop, metric registration, `/metrics` HTTP endpoint,
  `/healthz` probe. No write access to Dolt.
- `topology-health-metrics`: Per-rig desired vs actual convergence gauge, agent staleness
  gauge (seconds since `last_seen` per rig/role), pool-size delta (desired minus actual
  Polecats per rig), worktree health gauge.
- `beads-workflow-metrics`: Beads filed/closed/in_progress counters by type and priority,
  filed-to-closed latency histogram by Beads type (operation, escalation, cost), queue
  depth gauge (open Beads per assignee).
- `inline-reconciler-metrics`: Surveyor reconcile attempt counter (by outcome), convergence
  score gauge (latest, per reconcile run), verify retry count histogram, escalation reason
  counter (verify-exhausted / score-regression / dog-failure). Cost patrol run counter,
  action counter by (action, rig), ledger write duration histogram.
- `town-ctl-status-command`: `town-ctl status` subcommand — reads Dolt directly, calls
  `surveyor.ComputeScore`, renders per-rig health table. `--output=json` for scripting.
  Exit codes: 0=converged, 1=error, 2=not fully converged.
- `crd-status-enrichment`: `ConvergenceScore` (float64), `NonConverged` ([]string, max 20),
  `LastConvergenceAt` fields added to `GasTownStatus`. `FleetConverged` and
  `ActualTopologyAvailable` condition types added. Updated every 30s by status-sync loop.
- `grafana-dashboard`: `charts/grafana/gastown-fleet-health.json` — 5-row dashboard
  covering fleet overview, per-rig convergence timeline, Beads workflow, cost patrol,
  and Surveyor internals. Provisioned via ConfigMap when `grafana.enabled=true`.
- `alertmanager-rules`: `charts/templates/prometheusrule.yaml` — 6 alert rules across
  4 groups (convergence, staleness, beads, cost). All thresholds in `values.yaml`.
  Conditional on `prometheusRule.enabled=true`.

### Modified Capabilities

- `pkg/surveyor`: Add `metrics.go` with Prometheus collectors for the reconcile state
  machine. No change to reconcile logic.
- `pkg/townctl`: Add `status.go` + `status_format.go` (status command) and patrol
  metrics. No change to apply or patrol logic.
- `cmd/town-ctl`: Add `status` subcommand case.
- `pkg/k8s/operator/v1alpha1`: Add new fields to `GasTownStatus`.
- `pkg/k8s/operator/controllers`: Extend `patchStatusFromActual` to call `ComputeScore`.
- `charts/`: Observer Deployment + ServiceMonitor + RBAC + PrometheusRule + Grafana CM.

## Impact

- New binary: `dgt-observer` (Deployment: 1 replica, read-only Dolt access)
- New package: `pkg/observer`
- New subcommand: `town-ctl status`
- Modified packages: `pkg/surveyor`, `pkg/townctl`, `pkg/k8s/operator/v1alpha1`,
  `pkg/k8s/operator/controllers`
- Modified chart: 5 new template files + `values.yaml` additions
- No change to Dolt schema (all additions are read-only from Dolt's perspective)
- No change to Surveyor CLAUDE.md
- Depends on: `surveyor-topology-reconciler` (actual_topology tables must exist for
  topology-health-metrics to have data)
