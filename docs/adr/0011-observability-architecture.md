# ADR-0011: Observability Architecture for the DGT Control Plane

- **Status**: Accepted
- **Date**: 2026-03-21
- **Beads issue**: dgt-ytz (ADR task), dgt-47l (observability-monitor epic)

## Context

The dgt control plane implements a multi-layer convergence pipeline:

```
Desired state (town.toml / CRDs)
        ↓  town-ctl / gastown-operator
Dolt desired_topology tables
        ↓  Dolt change feed
Surveyor (AI reconciler) — reconcile/<uuid> branches
        ↓  Beads (bd create, bd dep add)
Dogs / Deacon (executor agents)
        ↓  actual_topology writes
Converged fleet state
```

Each layer has a well-defined interface (Dolt SQL, Beads), but none of the layers
emit metrics about their internal state. The current observability surface:

- `townctl_apply_*` counters/histograms (apply pipeline outcomes)
- `gastown_operator_reconcile_*` counters/histograms (K8s CRD reconcile)
- K8s `/healthz` / `/readyz` probes
- CRD `.status.conditions` fields
- Escalation Beads filed to Mayor on failure

This is sufficient to know that the operator is running and that `town-ctl apply`
completed. It is insufficient to know:

1. Whether the Surveyor is actively reconciling or stuck
2. What convergence score the fleet is achieving
3. How long Dog Beads take to execute
4. Which agents are stale and for how long
5. Whether cost patrol is firing and at what rate

## Goals / Non-Goals

**Goals:**
- Define where each class of metric is owned (observer binary vs inline)
- Design `dgt-observer`: a read-only Dolt+Beads polling service
- Add inline metrics to `pkg/surveyor` and `pkg/townctl`
- Define metric names, labels, and cardinality constraints
- Define the observer's deployment model (K8s + bare metal)

**Non-Goals:**
- Distributed tracing (OpenTelemetry) — deferred
- Log aggregation (Loki/ELK) — deferred
- Modifying Gas Town agent behavior to emit metrics — prohibited (no gt binary changes)
- Alerting rules and SLOs — left to operator configuration

## Decisions

### D1: Separate observer binary, not inline-only

A `dgt-observer` binary runs as a separate process (K8s Deployment or systemd unit),
polling Dolt for topology state and Beads for workflow state.

**Why not inline in the operator:**
The K8s operator reconciles CRD objects (GasTown, Rig, AgentRole, DoltInstance). It
does not read `actual_topology` tables — that is the Surveyor's domain. Adding
actual_topology polling to the operator would couple two concerns with different
consistency requirements.

**Why not inline in the Surveyor:**
The Surveyor is an LLM Claude Code agent. Its context budget is a finite resource.
Adding metric collection logic to the Surveyor CLAUDE.md increases token consumption
on every reconcile pass. The Surveyor should reason about topology, not maintain
Prometheus gauges. Inline metrics in `pkg/surveyor` (the Go library) are fine because
they are not in the LLM context — they are in the server-side Go process.

**Why not a Prometheus pushgateway:**
The Surveyor would push metrics at reconcile time (event-driven). But fleet health
metrics (agent staleness, convergence score) should reflect the current state of
`actual_topology`, not the last time the Surveyor ran. A pull-based observer reading
Dolt every 15 seconds provides continuous visibility independent of reconcile frequency.

**Decision**: `dgt-observer` is the canonical source for topology/fleet health metrics.
`pkg/surveyor` adds inline metrics for reconcile state machine events. Both are pull-based
Prometheus endpoints.

### D2: Dolt as the metrics source, not agent heartbeats

The observer reads `actual_topology`, `desired_topology`, and `bd_issues` (Beads) tables
directly from Dolt. It does not receive metrics from agents.

This is consistent with Gas Town's architecture: Dolt is the shared control plane. Any
reader of Dolt sees the authoritative state without requiring agent participation.

The alternative — having agents push metrics to Prometheus — requires the `gt` binary to
embed a Prometheus client. This is prohibited (ADR-0001: no `gt` modification).

### D3: Beads state read from Dolt, not SQLite

Beads uses dual storage: SQLite (local) + Dolt (remote). The observer reads from Dolt
because:
1. The observer may run on a different host than the Beads SQLite file
2. Dolt is already the connection point; adding a SQLite dependency adds a host-coupling
3. Dolt's `bd_issues` table is kept in sync by `bd dolt push/pull`

Latency trade-off: Dolt Beads state may lag SQLite by the `bd dolt push` interval
(typically seconds to minutes). For workflow timing metrics (filed-to-closed latency
histograms), this lag is acceptable — these metrics are used for trend analysis, not
real-time alerting.

### D4: Metric cardinality constraints

To prevent cardinality explosion:

- **Per-rig labels**: allowed. Rig count is bounded by `MaxRigs` (operator configuration,
  default 64). Labels: `rig` (rig name, slugified).
- **Per-role labels**: allowed for standard roles (mayor, polecat, witness, refinery,
  deacon, dog). Custom roles use `role="custom"` aggregate label.
- **Per-reconcile-uuid labels**: prohibited. Each reconcile UUID is unique → unbounded
  cardinality. Reconcile outcomes are counted in aggregate.
- **Per-bead-id labels**: prohibited. Use Bead type and priority as labels.
- **Per-agent-pid labels**: prohibited.

### D5: Observer K8s deployment model

The observer runs as a separate K8s Deployment (1 replica), not as a sidecar container
in the operator Pod.

**Rationale**: Fault isolation. If the observer crashes (e.g., Dolt schema change breaks
a query), the operator must continue reconciling. A sidecar would cause the operator Pod
to restart. A separate Deployment allows independent failure domains and rolling updates.

**RBAC**: The observer needs a `ClusterRole` with read-only access to Dolt (MySQL
protocol, external to K8s). No K8s API access required. The observer does not use
`controller-runtime`.

**Bare metal / laptop**: `systemd` unit file, same as the Surveyor pattern.

### D6: Convergence score ownership

`pkg/surveyor.ComputeScore()` is a pure function (no Dolt dep). The observer calls it
directly: read desired + actual from Dolt, call `ComputeScore`, expose as a gauge.

This avoids duplicating the scoring logic. The observer does not re-implement scoring —
it imports `pkg/surveyor` as a library.

The gauge is updated on every poll interval (default 15s), not only when the Surveyor
runs. This ensures the dashboard reflects real-time fleet state even when no reconcile
is in progress.

## Metric Catalogue

### dgt-observer exported metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `dgt_fleet_convergence_score` | Gauge | `rig` | Latest ComputeScore result per rig |
| `dgt_fleet_convergence_score_total` | Gauge | — | Aggregate score across all rigs |
| `dgt_agent_staleness_seconds` | Gauge | `rig`, `role` | Seconds since `actual_topology.last_seen` |
| `dgt_pool_size_desired` | Gauge | `rig` | desired_agent_config.max_polecats |
| `dgt_pool_size_actual` | Gauge | `rig` | Count of actual running polecats |
| `dgt_pool_size_delta` | Gauge | `rig` | desired − actual (positive = under-provisioned) |
| `dgt_worktrees_total` | Gauge | `rig`, `status` | Actual worktree count by status |
| `dgt_beads_open_total` | Gauge | `type`, `priority` | Open Beads by type and priority |
| `dgt_beads_filed_total` | Counter | `type`, `priority` | Cumulative Beads filed |
| `dgt_beads_closed_total` | Counter | `type`, `reason` | Cumulative Beads closed with reason |
| `dgt_beads_latency_seconds` | Histogram | `type` | filed-to-closed duration in seconds |
| `dgt_dolt_poll_duration_seconds` | Histogram | `query` | Dolt query execution time |
| `dgt_dolt_poll_errors_total` | Counter | `query` | Dolt query failure count |

### pkg/surveyor inline metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `surveyor_reconcile_total` | Counter | `outcome` | Reconcile attempts: success/escalated/abandoned |
| `surveyor_convergence_score` | Gauge | — | Score from last completed reconcile |
| `surveyor_verify_retries` | Histogram | — | Verify loop attempts per reconcile |
| `surveyor_escalations_total` | Counter | `reason` | Escalations: verify-exhausted/score-regression/dog-failure |
| `surveyor_reconcile_duration_seconds` | Histogram | `outcome` | End-to-end reconcile duration |
| `surveyor_stale_branch_cleanups_total` | Counter | — | Crash-recovery stale branch cleanups |

### pkg/townctl inline metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `townctl_patrol_runs_total` | Counter | `rig` | Cost patrol execution count per rig |
| `townctl_patrol_actions_total` | Counter | `action`, `rig` | warn/drain actions fired per rig |
| `townctl_patrol_budget_pct_used` | Gauge | `rig`, `budget_type` | Last observed budget % used per rig |
| `townctl_ledger_write_duration_seconds` | Histogram | — | Cost ledger write latency |

## Risks / Trade-offs

- **Dolt query load**: the observer adds read queries every 15s. Dolt uses InnoDB storage;
  read-only queries do not block write transactions. Mitigated by: read from a read replica
  if available, or configure longer poll interval for high-frequency setups.
- **Beads Dolt lag**: `bd dolt push` is not continuous. Beads workflow metrics may lag
  real state by minutes. Acceptable for trend analysis; documented in dashboard.
- **Convergence score on every poll vs Surveyor score on reconcile**: these may diverge
  if the Surveyor has custom LLM reasoning that overrides standard scoring. The observer
  uses `ComputeScore()` (deterministic); the Surveyor may use a modified threshold. Both
  are useful. Dashboard should display both when available.
- **Custom role staleness**: `dgt_agent_staleness_seconds` with `role="custom"` aggregates
  all custom roles. Per-role cardinality is bounded but may be large (up to 32 custom
  roles per town). Revisit if cardinality becomes an issue.

## Migration Plan

1. Add inline metrics to `pkg/surveyor` and `pkg/townctl` (no new binaries, no
   deployment changes — safe to ship early)
2. Implement `pkg/observer` and `cmd/dgt-observer` binary
3. Deploy `dgt-observer` as a separate systemd unit for bare metal testing
4. Add K8s Deployment + ServiceMonitor to chart
5. Import Grafana dashboard
