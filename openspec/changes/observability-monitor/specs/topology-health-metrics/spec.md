# Spec: Topology Health Metrics

## Purpose

Expose fleet health as Prometheus metrics by reading `desired_topology` and
`actual_topology` Dolt tables on every poll cycle. These metrics answer:
- Is the fleet converged right now?
- Which rigs or agent roles are stale?
- Is any rig under- or over-provisioned for Polecats?

## Source Tables

| Metric group | Source tables |
|-------------|---------------|
| Convergence score | `desired_*` + `actual_*` via `pkg/surveyor.ComputeScore` |
| Agent staleness | `actual_agent_config.last_seen` (per rig, per role) |
| Pool size | `desired_agent_config.max_polecats` + COUNT(actual Polecats running) |
| Worktree health | `actual_worktrees.status` (per rig) |

## Metric Definitions

### dgt_fleet_convergence_score

- **Type**: Gauge
- **Labels**: `rig` (slugified rig name)
- **Value**: `ComputeScore().Score` ∈ [0.0, 1.0]
- **Semantics**: 1.0 = fully converged. < 0.9 = intervention recommended.
  Updated every poll interval. Does not require Surveyor to be running.

### dgt_fleet_convergence_score_total

- **Type**: Gauge
- **Labels**: none
- **Value**: Weighted average of per-rig scores, weighted by rig's desired resource count.
  (A rig with 8 desired Polecats contributes more to the total than a rig with 1.)

### dgt_agent_staleness_seconds

- **Type**: Gauge
- **Labels**: `rig`, `role` (mayor/polecat/witness/refinery/deacon/dog/custom)
- **Value**: `now - actual_agent_config.last_seen` in seconds. 0 if no row exists (never seen).
- **Semantics**: > 60s (default `StaleTTL`) indicates the agent is considered unhealthy
  by the Surveyor's verify loop. Alert threshold: > 120s.

### dgt_pool_size_desired

- **Type**: Gauge
- **Labels**: `rig`
- **Value**: `desired_agent_config.max_polecats` for the rig.

### dgt_pool_size_actual

- **Type**: Gauge
- **Labels**: `rig`
- **Value**: COUNT of Polecat rows in `actual_agent_config` with `status='running'`.

### dgt_pool_size_delta

- **Type**: Gauge
- **Labels**: `rig`
- **Value**: `desired - actual`. Positive = under-provisioned. Negative = over-provisioned.
- **Semantics**: Persistent positive delta (> 2 minutes) suggests stuck spawn or exhausted
  worktrees.

### dgt_worktrees_total

- **Type**: Gauge
- **Labels**: `rig`, `status` (clean/dirty/unknown)
- **Value**: COUNT of rows in `actual_worktrees` per (rig, status).

## Cardinality Analysis

- `dgt_fleet_convergence_score`: bounded by rig count (max 64 by default)
- `dgt_agent_staleness_seconds`: bounded by `rigs × 7 standard roles` = 448 max time series
- `dgt_pool_size_*`: bounded by rig count (3 metrics × 64 rigs = 192 max)
- `dgt_worktrees_total`: bounded by `rigs × 3 status values` = 192 max

Total cardinality budget: ~900 time series. Well within Prometheus defaults (millions).

## Zero-Value Semantics

If a rig has no `actual_agent_config` rows (e.g., not yet deployed):
- `dgt_agent_staleness_seconds` = 0 (never seen, not stale per the metric definition)
- `dgt_pool_size_actual` = 0 (no running Polecats)
- `dgt_pool_size_delta` = desired value (fully under-provisioned)

Zero is meaningful for all these metrics and does not indicate metric absence.
