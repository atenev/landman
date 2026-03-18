# ADR-0007: `actual_topology` Dolt Schema Design

- **Status**: Proposed
- **Date**: 2026-03-18
- **Beads issue**: dgt-fkm
- **Deciders**: Aleksandar Tenev
- **Depends on**: ADR-0002 (Surveyor design), ADR-0003 (schema versioning)

---

## Context

ADR-0002 (Surveyor) requires an `actual_topology` Dolt schema that Gas Town
agents write to as they act. The Surveyor diffs `desired_topology` against
`actual_topology` to compute its reconcile plan. ADR-0002 Decision 4 requires
two-layer convergence verification (Dolt state + process health). The shape of
`actual_topology` tables determines what that diff can express.

This ADR answers four questions left open by ADR-0002:

1. What tables are in `actual_topology` and what columns do they carry?
2. Who writes `actual_topology`?
3. What is the staleness model (how does the Surveyor distinguish "not yet
   started" from "failed to start")?
4. Does `actual_topology` need its own versioning table
   (`actual_topology_versions`) parallel to ADR-0003?

---

## Decisions

### Decision 1: Tables mirror `desired_topology` shape for direct diffing

**Chosen**: Each `actual_topology` table has a 1-to-1 counterpart in
`desired_topology`. Column names and types align where the concepts align.
Actual-only columns (process-runtime data: `pid`, `status`, `last_seen`) are
additive.

| desired table              | actual table              |
|----------------------------|---------------------------|
| `desired_rigs`             | `actual_rigs`             |
| `desired_agent_config`     | `actual_agent_config`     |
| `desired_formulas`         | _(out of scope — schedules are not process state)_ |
| `desired_custom_roles` + `desired_rig_custom_roles` | `actual_custom_roles` |
| _(no desired counterpart)_ | `actual_worktrees`        |

**Rationale**: The Surveyor's diff logic — the most critical part of the
reconcile loop — becomes a straightforward SQL `FULL OUTER JOIN` (or equivalent
Dolt query) across matching column names. Adding a new topology resource requires
adding one desired and one actual table with aligned schemas, not re-teaching the
diff algorithm.

**`actual_worktrees` has no desired counterpart**: desired topology specifies
`max_polecats` (capacity), not individual worktrees. The Surveyor infers actual
worktree count from `actual_worktrees` rows and compares it to
`desired_agent_config.max_count`. The table exists to support process-health
layer verification (ADR-0002 Decision 4, Layer 2).

**`desired_formulas` has no actual counterpart**: Formulas are cron schedules.
Actual formula state is tracked as Bead lifecycle (did the scheduled Bead get
created and closed?), not as a live process with a PID. Formula convergence
verification is deferred to dgt-fqg.

---

### Decision 2: Deacon is the primary writer; agents self-report on spawn/exit

**Chosen**: Two-tier write model:

| Writer | What it writes | When |
|--------|---------------|------|
| Each agent process | Its own row in `actual_agent_config` or `actual_custom_roles` | On startup (status=`running`); on clean exit (status=`stopped`, then DELETE) |
| Polecat/Witness | `actual_worktrees` row | On worktree create; on worktree cleanup (DELETE) |
| Mayor | `actual_rigs` row | On rig initialisation (status=`starting`); Deacon transitions to `running` |
| Deacon | All tables | Heartbeat refresh of `last_seen`; status transitions for agents it cannot reach |

**Rationale for two-tier**:

Agent self-reporting gives precise startup and exit timestamps with low latency.
Deacon heartbeat provides crash detection: if an agent's process exits without a
clean self-report (crash), Deacon detects the absence during its next patrol
(`last_seen` TTL exceeded, OS process not found) and updates `status` to
`crashed`. This separation avoids a single point of failure: even if Deacon is
slow, every agent writes its own row on startup.

**Alternatives considered**:

| Option | Reason rejected |
|--------|----------------|
| Deacon-only writes | Deacon heartbeat interval (30s) means startup latency before actual state is visible. Self-reporting is near-instant. |
| Agent-only writes | Agents don't detect each other's crashes — a crashed agent never writes its own `crashed` status. Deacon patrol is required for crash detection. |

---

### Decision 3: Staleness model — `last_seen` TTL, not a separate stale table

**Chosen**: Every running-process row carries a `last_seen TIMESTAMP` refreshed
by Deacon heartbeat (default: every 30s). A row is considered **stale** when
`last_seen < NOW() - stale_ttl` (default: 60s, 2× heartbeat). The Surveyor
queries `last_seen` to distinguish states:

| status | last_seen | Surveyor interpretation |
|--------|-----------|------------------------|
| `starting` | fresh | Agent is launching — not yet a convergence failure |
| `starting` | stale | Failed to start — file remediation Bead |
| `running` | fresh | Agent is healthy |
| `running` | stale | Agent is unresponsive — file remediation Bead |
| `stopped` | any | Clean exit (row will be deleted soon by Deacon) |
| `failed` | any | Deacon has confirmed failure |
| `crashed` | any | Deacon detected absent process |
| row absent | n/a | Agent was never started or has been cleanly removed |

**Rationale**: A single `last_seen` column plus status enum handles all states
without a separate "stale" or "zombie" table. The Surveyor doesn't need to track
stale state itself — it reads the current `last_seen` and computes freshness
relative to the configured TTL at query time. Configurable TTL lets operators
tune for slow-start agents (e.g., Mayor on a large repo takes 20s to initialise).

**`stale_ttl` is a Surveyor configuration parameter**, not hardcoded in SQL. It
defaults to 2× Deacon heartbeat interval. Formal configuration spec deferred to
dgt-9tj (Surveyor CLAUDE.md).

---

### Decision 4: `actual_topology_versions` — yes, parallel to ADR-0003

**Chosen**: An `actual_topology_versions` table is created, parallel to
`desired_topology_versions` (ADR-0003). Deacon writes it as the first operation
of any `actual_topology` update. The Surveyor reads it before querying any
`actual_topology` table.

**Rationale**:

ADR-0003 Decision 1 explicitly deferred this question to dgt-fkm. The same logic
applies symmetrically:

- `actual_topology` tables have schema versions managed by Deacon and agent
  processes. If the Deacon binary is upgraded to write a new `actual_topology`
  schema that the Surveyor does not understand, the Surveyor must detect and
  refuse — not silently misinterpret data.
- The Surveyor pre-flight check reads `desired_topology_versions` (ADR-0003
  Decision 3). Adding `actual_topology_versions` to that pre-flight check
  costs one additional query and provides a symmetric correctness guarantee.

**The Surveyor is read-only on `actual_topology`**. It does not write, transition
status, or maintain heartbeats. Its role is exclusively to diff, plan, and
verify.

---

### Decision 5: Town-scoped custom roles use `rig_name = '__town__'` sentinel

**Chosen**: `actual_custom_roles` uses `rig_name = '__town__'` as the sentinel
for town-scoped roles (scope=`town` in `desired_custom_roles`). No separate
town-scoped table is introduced.

**Alternatives considered**:

| Option | Reason rejected |
|--------|----------------|
| Separate `actual_town_custom_roles` table | Two tables to query for custom role actual state — diff logic needs to union results. A sentinel value keeps the diff a single table scan. |
| NULL for rig_name | NULL in a primary key column violates relational normal form and is awkward in most SQL engines. |
| Separate boolean `is_town_scoped` column | An extra column to express what the primary key already carries is redundant. |

The `__town__` sentinel is documented in the column comment and enforced by a
CHECK constraint (`rig_name != ''`). A dedicated constraint
`chk_town_sentinel_not_empty` guards against the empty string without over-
constraining the sentinel value itself.

---

## Table Summary

### `actual_topology_versions`

```sql
table_name     VARCHAR(128) PK
schema_version INT
written_by     VARCHAR(128)   -- e.g. "deacon/heartbeat/0.1.0"
written_at     TIMESTAMP
```

### `actual_rigs`

```sql
name       VARCHAR(128) PK
repo       TEXT
branch     VARCHAR(256)
enabled    BOOLEAN
status     ENUM('starting','running','draining','stopped','failed')
last_seen  TIMESTAMP
updated_at TIMESTAMP
```

### `actual_agent_config`

```sql
rig_name   VARCHAR(128) PK
role       VARCHAR(128) PK   -- mayor|witness|refinery|deacon|polecat|<custom>
pid        BIGINT            -- NULL while starting or after clean stop
model      VARCHAR(256)      -- as reported by the running process
status     ENUM('starting','running','stopped','failed','crashed')
last_seen  TIMESTAMP
```

### `actual_worktrees`

```sql
rig_name   VARCHAR(128) PK
path       TEXT         PK(512)
branch     VARCHAR(256)
clean      BOOLEAN             -- git status is clean
status     ENUM('active','idle','stale')
bead_id    VARCHAR(128)        -- NULL when idle
last_seen  TIMESTAMP
```

### `actual_custom_roles`

```sql
rig_name        VARCHAR(128) PK   -- '__town__' for scope=town roles
role_name       VARCHAR(128) PK
instance_index  INT          PK   -- 0-based; >0 when max_instances > 1
pid             BIGINT
status          ENUM('starting','running','stopped','failed','crashed')
last_seen       TIMESTAMP
```

---

## Consequences

### What becomes easier

- **Surveyor diff**: aligned column shapes between desired and actual tables make
  the reconcile diff a straightforward SQL join per table. No translation layer.
- **Crash detection**: `last_seen` TTL + Deacon patrol gives the Surveyor a clean
  "healthy / stale / crashed" signal without polling the OS process list itself.
- **multi-instance custom roles**: `instance_index` in `actual_custom_roles` PK
  supports `max_instances > 1` without schema changes.
- **Audit history**: every Dolt commit on `actual_topology` tables is queryable.
  "When did rig X fail?" is a Dolt log query.

### New constraints introduced

- **Deacon must write `actual_topology_versions` before any other actual_topology
  update**. Same contract as ADR-0003 for `town-ctl`. Any future agent that
  writes `actual_topology` must respect this constraint.
- **`__town__` sentinel is a convention**. It must be documented in Deacon's
  CLAUDE.md and the Surveyor's CLAUDE.md. Enforcement relies on the CHECK
  constraint (empty string only) plus documentation — there is no SQL-level
  enforcement that only town-scoped roles use `'__town__'`.
- **`path` prefix key in `actual_worktrees`**: `PRIMARY KEY (rig_name,
  path(512))` requires path values to be representable in 512 bytes (UTF-8).
  Paths longer than 512 bytes on unusual filesystems will fail inserts — an
  acceptable constraint given typical path lengths.
- **Stale TTL is a Surveyor configuration parameter** not represented in the
  schema. Operators must ensure Deacon heartbeat interval and Surveyor stale TTL
  are configured consistently. Default (2× heartbeat = 60s) is documented but
  not enforced.

### Out of scope for this ADR

- Deacon heartbeat implementation and heartbeat interval configuration (→ dgt-9tj)
- Surveyor diff algorithm and reconcile plan generation (→ dgt-9tj)
- Convergence scoring thresholds (→ dgt-fqg)
- Formula actual state tracking (→ dgt-fqg)
- SQL migration file (→ dgt-7ve, implemented in migrations/004_actual_topology.sql)
