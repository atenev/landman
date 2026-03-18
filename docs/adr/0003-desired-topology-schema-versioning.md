# ADR-0003: `desired_topology` Schema Versioning Strategy

- **Status**: Proposed
- **Date**: 2026-03-17
- **Beads issue**: dgt-pe8
- **Deciders**: Aleksandar Tenev
- **Amends**: ADR-0001 (Decision 2 consequences), ADR-0002 (Decision 1 consequences)

---

## Context

ADR-0001 established Dolt as the actuator coupling point and introduced `desired_topology`
tables as the versioned public contract between `town-ctl` and the Surveyor. Both ADR-0001
and ADR-0002 specified a per-row `schema_version INT NOT NULL DEFAULT 1` column on every
`desired_topology` table. The stated intent was to let the Surveyor detect and hard-fail on
unknown schema versions written by a newer `town-ctl`.

This convention has a structural flaw that becomes apparent when designing the first new
topology tables (dgt-9ft, dgt-9cm): `schema_version` is a DDL-level property. A column
addition, removal, or type change in a Dolt table affects every row simultaneously — there
is no such thing as a "v1 row" coexisting with a "v2 row" in a MySQL-compatible table. The
per-row column models a table-level fact at the wrong granularity.

The specific failure mode: if a mixed-version state somehow exists (two `town-ctl` versions
writing to the same table), the Surveyor has no clean resolution path. Hard-failing on the
first unknown-version row leaves all subsequent rows unreconciled. Soft-skipping unknown rows
silently ignores work. Neither outcome is correct — and mixed-version states are the exact
scenario per-row versioning was designed to handle.

This ADR is being written before any `desired_topology` DDL has been implemented. The cost of
correcting the convention is editing prose in two ADRs. The cost of correcting it after
implementation is writing migration tooling across all topology tables simultaneously.

---

## Decisions

### Decision 1: A `desired_topology_versions` table as the single versioning authority

**Chosen**: Replace per-row `schema_version` columns with a dedicated
`desired_topology_versions` table. One row per topology table. Written by `town-ctl` as the
first operation in every apply transaction. Read by the Surveyor as the first operation in
every reconcile loop.

**Alternatives considered**:

**Option A — Per-row `schema_version` (original ADR-0001/0002 convention)**

```sql
-- on every desired_topology table:
schema_version INT NOT NULL DEFAULT 1
```

Rejected because:
- Schema version is a table-level DDL property. Per-row storage is the wrong abstraction.
  A column added in v2 either exists for all rows or for none — the DDL change is atomic at
  the table level.
- A mixed-version table (v1 and v2 rows) has no clean Surveyor resolution path. The state
  that per-row versioning is meant to detect cannot be reasoned about correctly after the fact.
- Every Surveyor topology query must include a `schema_version` check per row — noise on the
  hot path for every reconcile loop iteration.
- Dolt diffs show `schema_version` changing on every row on every apply — obscuring what
  actually changed.

**Option B — `desired_topology_versions` table (chosen)**

```sql
CREATE TABLE desired_topology_versions (
  table_name     VARCHAR(128) NOT NULL,
  schema_version INT          NOT NULL,
  written_by     VARCHAR(128),   -- e.g. "town-ctl/0.2.0"
  written_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (table_name)
);
```

Chosen because:
1. **Correct abstraction**: one row per table correctly models version as a table-level
   property. The version is the same for all rows because the DDL is the same for all rows.
2. **Single pre-flight check**: the Surveyor reads this table once before touching any
   topology data. O(tables) rows, clear go/no-go. No per-row version checks scattered
   across queries.
3. **Free audit trail**: `written_by` and `written_at` record which `town-ctl` version wrote
   each table and when. Useful for debugging "why did the Surveyor refuse to reconcile."
4. **Readable Dolt diffs**: one row per table changes per schema bump.
   `desired_topology_versions: desired_custom_roles 1→2` — immediately clear.
5. **No per-topology-table column to maintain**: adding a new topology table requires only
   a `desired_topology_versions` upsert in `town-ctl`, not a new column in every table.

---

### Decision 2: `town-ctl` writes `desired_topology_versions` first in every transaction

**Chosen**: `town-ctl` upserts `desired_topology_versions` rows (one per topology table
being written) as the **first** SQL statement in every apply transaction, before any
topology data rows.

**Rationale**:

Transaction ordering guarantees that if the apply transaction succeeds, the version record
and the data rows are always consistent — the version row is never absent when data rows
exist. If the transaction fails and rolls back, neither the version record nor the data rows
are committed. There is no window where data rows exist without a version record.

`written_by` is populated from the `town-ctl` binary version string at build time (e.g.,
`"town-ctl/0.2.0"`). It is informational — the Surveyor does not act on it; it acts on
`schema_version` only.

---

### Decision 3: Surveyor reads `desired_topology_versions` as the first operation in every reconcile loop

**Chosen**: At the start of every reconcile loop iteration (startup, change-feed event,
crash recovery), the Surveyor reads `desired_topology_versions` before querying any topology
table. If any table has an unknown `schema_version`, the Surveyor hard-fails the reconcile
attempt and files a high-priority escalation Bead to Mayor before reading any topology rows.

**Rationale**:

This is the correctness guarantee: the Surveyor never acts on topology data it cannot
correctly interpret. A version mismatch means `town-ctl` was upgraded and wrote a schema the
Surveyor does not understand. Acting on partially-understood data is worse than not acting.
The Mayor escalation Bead provides the operator with full context: which table, which version
was found, which version was expected.

This pre-flight check is O(tables) — negligible overhead on every reconcile loop iteration
in exchange for a strong correctness guarantee.

---

## Consequences

### What becomes easier

- **New topology table design**: adding a new `desired_topology` table requires no new
  `schema_version` column — only a `desired_topology_versions` upsert in `town-ctl`.
- **Debugging version mismatches**: `written_by` and `written_at` in
  `desired_topology_versions` give operators an immediate answer to "what wrote this and
  when."
- **Surveyor pre-flight**: a single query before any reconcile work gives a clear go/no-go
  on schema compatibility.
- **Dolt audit clarity**: one row change per schema bump per table, not per-row noise.

### New constraints introduced

- **`desired_topology_versions` must be the first write in every `town-ctl` transaction**.
  Any future actuator (K8s operator, GitOps controller) writing `desired_topology` tables
  must also write `desired_topology_versions` first. This is a contract, not a convention.
- **ADR-0001 and ADR-0002 are amended**: the per-row `schema_version` consequence in both
  ADRs is superseded by this ADR. No `desired_topology` table designed after this ADR
  carries a per-row `schema_version` column.
- **`actual_topology` versioning**: the Surveyor writes `actual_topology` tables. A parallel
  `actual_topology_versions` table may be warranted by the same logic. Decision deferred to
  dgt-fkm (actual_topology schema design).

### Out of scope for this ADR

- `desired_topology` table DDL content (→ dgt-9ft, dgt-uxa)
- `actual_topology` versioning strategy (→ dgt-fkm)
- `town-ctl` binary implementation (→ dgt-apu)
