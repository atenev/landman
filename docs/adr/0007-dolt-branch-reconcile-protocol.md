# ADR-0007: Dolt Branch Reconcile Transaction Protocol

- **Status**: Proposed
- **Date**: 2026-03-18
- **Beads issue**: dgt-wv5
- **Depends on**: ADR-0002 (Decision 3), ADR-0003
- **Deciders**: Aleksandar Tenev

---

## Context

ADR-0002 Decision 3 established that each Surveyor reconcile attempt operates on a
Dolt branch `reconcile/<uuid>`, merging to `main` on success and abandoning on failure.
It explicitly deferred the formal protocol to this ADR:

> "Stale branch TTL must be configurable and its default chosen carefully: too short and
>  a slow drain triggers a false crash-recovery; too long and a genuinely crashed reconcile
>  blocks the next one (→ dgt-wv5)."

This ADR answers the five open questions from dgt-wv5:

1. Branch naming convention and queryability tradeoffs
2. Merge commit message format
3. Abandoned branch retention policy
4. Concurrent reconcile guard and crash-recovery protocol
5. Dolt branch read isolation semantics

---

## Decisions

### Decision 1: Branch naming is `reconcile/<uuid-v4>`; no timestamp or rig-name in branch name

**Chosen**: `reconcile/<uuid-v4>` — e.g. `reconcile/a3f2e1b0-4c7d-4e8f-9a0b-1c2d3e4f5a6b`.

**Alternatives considered**:

| Option | Reason rejected |
|--------|-----------------|
| `reconcile/<timestamp>/<rig-name>` | Timestamp in branch name couples naming to clock skew; rig-name implies single-rig scope, but a reconcile pass covers all rigs. Query by time is better served by `reconcile_log.started_at` column than by branch name parsing. |
| `reconcile/<timestamp>` | Still clock-coupled; no uniqueness guarantee across parallel instances; harder to join against `reconcile_log` than a UUID foreign key. |
| `reconcile/<rig-name>/<uuid>` | Rig-name prefix suggests the branch owns rig-specific state; it does not — Dogs write rig changes to `main`, and a single reconcile pass may span all rigs. |

**Rationale**:

The UUID is the primary key of the reconcile attempt in `reconcile_log`. Using it directly as
the branch name makes the join trivial:

```sql
SELECT * FROM dolt_branches
WHERE name LIKE 'reconcile/%';

-- Correlate with plan metadata:
SELECT r.name, l.started_at, l.convergence_score
FROM dolt_branches r
JOIN reconcile_log l ON r.name = CONCAT('reconcile/', l.reconcile_uuid)
ORDER BY l.started_at DESC;
```

Queryability by time, rig, or outcome is achieved through `reconcile_log` columns, not branch
name encoding. This keeps branch names stable (no format changes if we add fields to the log)
and avoids string-parsing fragility in queries.

---

### Decision 2: Merge commit message format

**Chosen**: Structured, single-line summary with a multi-line body.

```
reconcile: <uuid> score=<0.00–1.00> ops=<N> duration=<Ns>

reconcile_uuid: <uuid>
convergence_score: <0.00–1.00>
operation_count: <N>
duration_seconds: <N>
started_at: <RFC3339>
completed_at: <RFC3339>
desired_snapshot_commit: <dolt-commit-hash>
```

**Rationale**:

`dolt log --oneline` shows the summary line — operators see UUID, score, ops, and duration
at a glance without scrolling. The structured body fields are machine-parseable by `dolt log`
grep or by a monitoring script. `desired_snapshot_commit` links the merge commit to the exact
`desired_topology_versions` row that triggered this reconcile, enabling full auditability
("what was the desired state when this reconcile ran?").

The `actual_snapshot_commit` is omitted from the merge commit message intentionally: the
merge itself is the snapshot — `dolt diff main~1 main` shows the exact actual_topology delta.

---

### Decision 3: Abandoned branch retention — 7-day window, summarised into `reconcile_archive`

**Chosen**: Abandoned branches are retained for 7 days, then pruned. Before pruning, the
Surveyor writes a summary row to `reconcile_archive` on `main`. The summary includes UUID,
reason, started_at, and a JSON blob of the `reconcile_log` rows from the abandoned branch.
This preserves the audit record indefinitely at a fraction of the branch storage cost.

**Retention window default**: 7 days. Configurable via Surveyor CLAUDE.md.

**`reconcile_archive` table schema**:

```sql
CREATE TABLE reconcile_archive (
    reconcile_uuid   VARCHAR(36)  NOT NULL PRIMARY KEY,
    reason           VARCHAR(255) NOT NULL, -- 'surveyor-crash' | 'dog-failure' | 'verify-exhausted'
    started_at       DATETIME     NOT NULL,
    abandoned_at     DATETIME     NOT NULL,
    log_json         JSON         NOT NULL  -- full reconcile_log rows serialised as JSON
);
```

**Why not keep branches forever?**

Dolt branch count is not free. Each branch is a named reference in the noms storage engine.
A system running 10 reconciles/day accumulates 70+ abandoned branches per week in a degraded
environment. Beyond storage, `dolt branch -a` becomes noisy and `LIKE 'reconcile/%'` queries
slow down as the reference list grows.

**Why not delete immediately?**

7 days gives operators time to inspect a failed reconcile branch directly — running `dolt diff`,
reading `reconcile_log` rows, correlating with Dog Bead history — without requiring the archive
JSON. Immediate deletion sacrifices debuggability for cleanliness; 7 days balances both.

**Pruning timing**: Surveyor startup, after stale-branch cleanup, before the first reconcile.
This ensures pruning is GUPP-compliant: it runs unconditionally on restart, not only when a
new reconcile is triggered.

---

### Decision 4: Stale-branch TTL = 30 min; advisory-lock TTL = 15 min; both configurable

**Stale-branch TTL (default 30 minutes)**:

A branch is declared stale when its `reconcile_log.started_at` timestamp is older than the TTL
and the branch has not been merged. Stale branches are abandoned with reason `surveyor-crash`.

**Rationale for 30 minutes**: A reconcile involving a slow Polecat drain (e.g. a rig with
30 active Polecats each completing a multi-file refactor) may legitimately run for 10–25 minutes
end-to-end. 30 minutes gives 5+ minutes of headroom for slow drains while ensuring a genuinely
crashed Surveyor is detected within one reconcile cycle. Setting TTL < 10 minutes risks false
crash-recovery on normal long reconciles; > 60 minutes delays recovery after a real crash.

**Advisory-lock TTL (default 15 minutes)**:

A live Surveyor refreshes the `surveyor_lock` row at each major reconcile stage (branch open,
Beads filed, verify loop started). A 15-minute-old lock is unambiguously from a dead Surveyor,
not a slow one. Setting this shorter than 15 minutes risks false lock-theft on a temporarily
suspended process (e.g. host sleep); longer than 15 minutes delays parallel-instance recovery.

**Configuration surface** (in Surveyor CLAUDE.md):

```toml
[reconcile]
stale_branch_ttl_minutes    = 30   # default
advisory_lock_ttl_minutes   = 15   # default
abandoned_branch_retain_days = 7   # default
```

---

### Decision 5: Reconcile branch provides plan-isolation reads; verify loop reads from `main`

**Chosen**: The reconcile branch is used for plan metadata audit only. The Surveyor writes
`reconcile_log` rows to the branch and reads them back on the branch for coherence. For the
verify loop, the Surveyor switches its Dolt session to `main` to read `actual_topology`.

**What Dolt branch isolation means here**:

Dolt's branch model is git-for-SQL: writes to branch `reconcile/<uuid>` are immediately visible
to SELECT queries on that branch but are invisible to other branches until merged. This gives
the Surveyor coherent read-your-writes semantics for its plan metadata without affecting `main`.

**Why Dogs write to `main`, not the reconcile branch**:

Dogs execute topology operations (rig add/remove, agent start/stop) and record results in
`actual_topology`. These writes must be immediately visible to other Gas Town participants
(Mayor, Deacon, other Dogs) — not locked inside the Surveyor's branch. Writing to `main`
ensures operational correctness. The reconcile branch is an audit log, not a staging area.

**Verify loop isolation**:

The verify loop queries `actual_topology` from `main` to see the real post-operation state.
If the Surveyor queried the reconcile branch for actual_topology, it would always see the
pre-reconcile snapshot (Dogs never wrote there). The session switch to `main` for verify
reads is explicit and documented in the Surveyor CLAUDE.md.

```sql
-- Plan phase: write audit record to reconcile branch
USE `reconcile/a3f2e1b0...`;
INSERT INTO reconcile_log (reconcile_uuid, phase, ...) VALUES (...);

-- Verify phase: read actual state from main
USE main;
SELECT * FROM actual_topology WHERE ...;
```

---

## `reconcile_log` Table Schema

All plan metadata and verification results are stored in `reconcile_log` on the
reconcile branch (rows visible on branch, merged to main on success):

```sql
CREATE TABLE reconcile_log (
    reconcile_uuid    VARCHAR(36)  NOT NULL,
    phase             VARCHAR(32)  NOT NULL,  -- 'plan' | 'verify' | 'merge' | 'abandon'
    logged_at         DATETIME     NOT NULL,
    -- Plan phase fields (populated when phase='plan'):
    desired_snapshot  JSON,    -- desired_topology rows at plan time
    bead_ids          JSON,    -- array of operation Bead IDs filed
    operation_count   INT,
    -- Verify phase fields (populated when phase='verify'):
    actual_snapshot   JSON,    -- actual_topology rows at verify time
    convergence_score DECIMAL(4,3),
    duration_seconds  INT,
    -- Abandon fields (populated when phase='abandon'):
    reason            VARCHAR(255),  -- 'surveyor-crash' | 'dog-failure' | 'verify-exhausted'
    PRIMARY KEY (reconcile_uuid, phase)
);
```

---

## `surveyor_lock` Table Schema

```sql
CREATE TABLE surveyor_lock (
    lock_id       INT          NOT NULL PRIMARY KEY DEFAULT 1,  -- singleton row
    holder_pid    INT          NOT NULL,
    holder_host   VARCHAR(255) NOT NULL,
    acquired_at   DATETIME     NOT NULL,
    refreshed_at  DATETIME     NOT NULL
    -- CHECK (lock_id = 1) enforces singleton
);
```

The Surveyor upserts this row at startup (acquiring the lock) and updates `refreshed_at`
at each reconcile stage. On graceful shutdown it deletes the row.

---

## Consequences

### What becomes easier

- **Full reconcile audit**: every attempt (success or failure) is queryable via
  `reconcile_log`. The `reconcile_archive` table extends this indefinitely after branch pruning.
- **Crash-safe recovery**: stale-branch detection is deterministic and TTL-configurable.
  No event replay needed — state drives recovery.
- **Parallel-safe**: the `surveyor_lock` singleton prevents split-brain reconciles.
  Stale lock detection handles crashed Surveyors without manual intervention.
- **Operational observability**: `dolt log --oneline main | grep '^reconcile:'` gives a
  running summary of all successful reconciles. Abandoned branch count indicates environment
  health.

### New constraints introduced

- **`reconcile_log` DDL** must be added to migrations before the Surveyor is deployed.
- **`surveyor_lock` DDL** same requirement.
- **`reconcile_archive` DDL** same requirement.
- **Surveyor session management**: the verify loop must switch its Dolt connection context
  from the reconcile branch to `main` for `actual_topology` reads. This must be specified
  precisely in the Surveyor CLAUDE.md (→ dgt-9tj) to prevent verify-on-wrong-branch bugs.
- **Lock refresh**: the Surveyor must refresh `surveyor_lock.refreshed_at` at each major
  stage. A Surveyor that hangs between refreshes for > 15 minutes risks lock theft by a
  restarted instance. The CLAUDE.md must specify refresh points explicitly (→ dgt-9tj).

### Out of scope for this ADR

- `reconcile_log` DDL migration file (→ dgt-7ve or new issue)
- `surveyor_lock` DDL migration file (→ dgt-7ve or new issue)
- `reconcile_archive` DDL migration file (→ dgt-7ve or new issue)
- Surveyor CLAUDE.md content specifying the exact Dolt session-switching protocol (→ dgt-9tj)
- Convergence scoring thresholds per resource type (→ dgt-fqg)
- Mid-reconcile desired_topology change protocol (→ dgt-9tj)
