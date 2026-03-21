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
- Kubernetes: `GasTownObserver` added to the operator Deployment, plus a `ServiceMonitor`
  for Prometheus scraping.
- Grafana dashboard definition in `charts/grafana/` for fleet health visualization.
- ADR-0011: documents the observability architecture decision (separate binary vs inline).

## Capabilities

### New Capabilities

- `dgt-observer`: The observer binary — CLI flags (`--dolt-dsn`, `--beads-db`, `--interval`,
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

### Modified Capabilities

- `pkg/surveyor`: Add `metrics.go` with Prometheus collectors for the reconcile state
  machine. No change to reconcile logic.
- `pkg/townctl`: Add patrol metrics to `patrol.go`. No change to patrol logic.
- `cmd/operator`: Wire observer as a separate goroutine if `--observer-addr` flag is set,
  or deploy as a separate Deployment (default for K8s).

## Impact

- New binary: `dgt-observer` (Deployment: 1 replica, read-only Dolt access)
- New package: `pkg/observer`
- Modified packages: `pkg/surveyor` (metrics.go added), `pkg/townctl` (patrol metrics)
- Modified chart: `charts/` — observer Deployment + ServiceMonitor + RBAC ClusterRole
- No change to Dolt schema (observer is read-only)
- No change to Surveyor CLAUDE.md (metrics are added to Go struct, not agent context)
- Depends on: `surveyor-topology-reconciler` (actual_topology tables must exist for
  topology-health-metrics to have data)
