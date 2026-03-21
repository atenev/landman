# Spec: Beads Workflow Metrics

## Purpose

The Beads coordination layer is the execution backbone of the dgt control plane:
- Surveyor files operation Beads → Dogs execute → actual_topology updated
- Cost patrol files warn/drain Beads → Mayor or Deacon acts
- Escalation Beads are filed when convergence fails

Currently there is no visibility into: how many Beads are open, how long they take
to close, or whether a specific assignee (e.g., Dogs) is falling behind.

These metrics answer:
- Are operation Beads being picked up and closed quickly?
- Is there a Beads queue buildup (Dogs overwhelmed)?
- How often do escalation Beads fire?
- What is the p50/p99 time from "Surveyor files Bead" to "Dog closes Bead"?

## Source

All metrics read from the Dolt `bd_issues` table (same Dolt DSN as topology reads).

Relevant columns: `id`, `type`, `priority`, `status`, `created_at`, `closed_at`.
Beads types used by dgt: `task` (operation Beads), `bug`, `feature` (general),
`epic`. For dgt-specific workflow, filter by tag or title prefix (e.g., `"RECONCILE"`,
`"COST"`). Implementation can use coarse type bucketing initially and refine later.

## Metric Definitions

### dgt_beads_open_total

- **Type**: Gauge
- **Labels**: `type` (task/bug/feature/epic), `priority` (0–4)
- **Value**: COUNT of open + in_progress Beads of each (type, priority).
- **Semantics**: A growing queue of `task` Beads at P1 with no decrease over time
  indicates Dogs are not processing operation Beads.

### dgt_beads_filed_total

- **Type**: Counter
- **Labels**: `type`, `priority`
- **Value**: Monotonically increasing count of all Beads ever filed.
- **Implementation**: Set to COUNT(*) of all rows with that type/priority on each poll.
  Since Beads are never deleted, this is a counter that only increases.

### dgt_beads_closed_total

- **Type**: Counter
- **Labels**: `type`, `close_reason` (done/wontfix/duplicate/unknown)
- **Value**: Monotonically increasing count of closed Beads by close reason.

### dgt_beads_latency_seconds

- **Type**: Histogram
- **Labels**: `type`
- **Buckets**: [1, 5, 15, 30, 60, 120, 300, 600, 1800, +Inf] (seconds)
- **Value**: `closed_at - created_at` in seconds for Beads closed within the poll window.
- **Semantics**: p50 < 60s = healthy operation Bead throughput. p99 > 300s = Dogs slow.
- **Implementation**: On each poll, query Beads closed within `2 × poll_interval`. This
  ensures no closed Beads are missed between polls (with 2× overlap as buffer). Avoid
  double-counting: track the latest `closed_at` seen and only query newer rows.

### dgt_beads_queue_depth_by_assignee

- **Type**: Gauge
- **Labels**: `assignee` (agent username/role name)
- **Value**: COUNT of in_progress Beads assigned to each assignee.
- **Semantics**: Useful to detect if a specific Dog instance is overwhelmed.
- **Cardinality note**: assignee is bounded by agent count (max ~30 per rig). Label is
  safe if Bead assignees use stable names (rig-name-prefixed roles, not PIDs).

## Poll Window Implementation

The `ReadBeads` function takes a `window time.Duration` parameter. To correctly
populate the latency histogram:

```
latency samples = SELECT type, TIMESTAMPDIFF(SECOND, created_at, closed_at)
                  FROM bd_issues
                  WHERE status = 'closed'
                    AND closed_at > NOW() - INTERVAL window SECOND
                    AND closed_at > last_seen_closed_at
```

`last_seen_closed_at` is tracked in the `BeadsSnapshot` between polls to avoid
re-observing the same Bead in the histogram twice.

## Cardinality Analysis

- `dgt_beads_open_total`: `4 types × 5 priorities` = 20 time series
- `dgt_beads_filed_total`: `4 types × 5 priorities` = 20 time series
- `dgt_beads_closed_total`: `4 types × 4 reasons` = 16 time series
- `dgt_beads_latency_seconds`: `4 types × 10 histogram buckets` ≈ 40 time series
- `dgt_beads_queue_depth_by_assignee`: bounded by active agent count (< 100)

Total: ~200 time series for Beads metrics.
