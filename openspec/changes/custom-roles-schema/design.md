## Context

Gas Town is an AI-native multi-agent coding orchestrator (~189k lines of Go). Its 7 built-in agent
roles (Mayor, Polecats, Witness, Refinery, Deacon, Dogs, Crew) are hardcoded in the `gt` binary.
Operators have no mechanism to define new agent archetypes without modifying `gt`.

ADR-0001 established `town.toml` as the declarative manifest format and Dolt as the shared state
plane. ADR-0002 established the Surveyor as the external reconciler. Both ADRs reserved
`[[rig.role]]` as a future extension slot (dgt-bfp) but specified per-row `schema_version` on
every `desired_topology` table — a convention that conflates writer version with schema version,
a table-level DDL property.

Constraint: **no `gt` binary modification**. Custom roles must be declarable, storable in Dolt,
and reconcilable by the Surveyor without any change to Gas Town internals.

## Goals / Non-Goals

**Goals:**
- Define `[[role]]` TOML block schema and per-rig opt-in via `[rig.agents].roles`
- Define `desired_topology_versions` table as the single versioning authority for all `desired_topology` tables
- Define `desired_custom_roles` and `desired_rig_custom_roles` Dolt DDL
- Amend ADR-0001 and ADR-0002 to reflect the versioning strategy change
- Enforce file-reference-only for role CLAUDE.md (no inline content)

**Non-Goals:**
- `town-ctl` Go implementation (→ dgt-apu)
- `Role` Go struct and JSON Schema definitions (→ dgt-lai)
- Annotated town.toml examples with custom roles (→ dgt-rlf)
- `actual_custom_roles` Dolt table (→ dgt-fkm, Surveyor scope)
- CLAUDE.md template inheritance for roles (→ dgt-90x, V2)
- Custom roles replacing built-in roles (requires `gt` modification)

## Decisions

### D1: `[[role]]` as global definitions, per-rig opt-in via `[rig.agents].roles`

Two structural options considered:

| Option | Structure | Tradeoff |
|--------|-----------|----------|
| A (chosen) | Global `[[role]]` + `[rig.agents].roles = [...]` | Define once, enable per rig. No duplication. |
| B (rejected) | `[[rig.role]]` inline per rig | Repeats full role spec per rig. Divergence risk across rigs sharing the same role. |

Option A treats role definitions like `[defaults]` — global, with per-rig opt-in. A role defined
globally but not listed in any rig's `roles` array is valid (defined but inactive). This mirrors
how `[defaults]` applies until a rig overrides — clean mental model.

### D2: `desired_topology_versions` table — Option B over per-row `schema_version`

Per-row `schema_version` (ADR-0001/0002 convention) is the wrong abstraction: DDL changes affect
all rows in a table equally. A v2 column addition either exists for all rows or for none — there
is no "v1 row in a v2 table." Per-row versioning can only detect mixed-version states, not prevent
them, and has no clean resolution path when detected.

`desired_topology_versions` (one row per table: `table_name PK, schema_version, written_by,
written_at`) correctly models version as a table-level property:

- `town-ctl` upserts this table **first** in every transaction, before any topology writes
- Surveyor reads this table **first** before any topology query — one pre-flight check
- Dolt diffs show `desired_topology_versions: desired_custom_roles 1→2` — human-readable audit
- `written_by` (`"town-ctl/0.2.0"`) + `written_at` provide a free apply audit trail

**Breaking change**: ADR-0001 and ADR-0002 consequences sections must be amended. All future
`desired_topology` table DDL must omit per-row `schema_version`. The `desired_topology_versions`
table applies retroactively to all topology tables, including those designed in dgt-9ft.

### D3: `desired_custom_roles` uses `name` as primary key

UUID PK produces unreadable Dolt diffs. `name` is the natural unique key — it is what Beads,
CLAUDE.md content, and rig associations reference. Using it as PK makes the audit log
human-readable. Role renames (rare) require a cascade update to `desired_rig_custom_roles` — the
FK makes the dependency explicit.

### D4: Explicit `trigger_schedule` / `trigger_event` columns with CHECK constraint

A single `trigger_spec JSON` column is opaque to SQL queries and unvalidated at the DB layer.
Two explicit columns (`trigger_schedule VARCHAR(64)`, `trigger_event VARCHAR(128)`) enable:

```sql
CONSTRAINT chk_trigger CHECK (
  (trigger_type = 'schedule' AND trigger_schedule IS NOT NULL) OR
  (trigger_type = 'event'    AND trigger_event    IS NOT NULL) OR
  (trigger_type IN ('bead_assigned', 'manual'))
)
```

This enforces the trigger invariant at the Dolt layer, not just in `town-ctl` validation.

### D5: `parent_role` validated as string in `town-ctl`, not FK

`parent_role` references either a built-in role name (`mayor`, `witness`, `deacon`, etc.) or
another custom role `name`. A FK to a `known_roles` enumeration table would require maintaining
that table in sync with Gas Town's built-in role set — coupling Dolt schema to `gt` internals.
`town-ctl` validates `parent_role` against a hardcoded list of built-in names plus parsed custom
role names at apply time. This is sufficient and avoids the coupling.

### D6: Town-scoped roles need no junction table

`scope = 'town'` roles are always active if they exist in `desired_custom_roles`. No
`desired_town_custom_roles` junction table needed. The Surveyor queries by scope:

```sql
-- Rig-scoped: join junction table
SELECT r.* FROM desired_custom_roles r
JOIN desired_rig_custom_roles rr ON rr.role_name = r.name
WHERE r.scope = 'rig' AND rr.rig_name = ? AND rr.enabled = TRUE;

-- Town-scoped: no junction, always active
SELECT * FROM desired_custom_roles WHERE scope = 'town';
```

### D7: `claude_md_path NOT NULL` — file references enforced at schema level

No `claude_md_content TEXT` column. Inline CLAUDE.md content in TOML is rejected at the schema
level, not just by convention. `town-ctl` resolves and validates the path exists at apply time.

## Risks / Trade-offs

- **Breaking versioning convention** → ADR-0001 and ADR-0002 are both `Proposed` with no
  implementation yet. Cost of amendment is prose edits only. Deferring the fix means writing
  migration tooling later across all topology tables simultaneously. Fix now while cost is zero.
- **FK on `desired_rig_custom_roles.role_name`** → `town-ctl` must write `desired_custom_roles`
  rows before `desired_rig_custom_roles` in the same transaction. This is a transaction ordering
  constraint, not a correctness risk — Dolt transactions are ACID.
- **`parent_role` as validated string** → if a built-in role is renamed in a future `gt` version,
  existing `parent_role` values silently become invalid. Mitigated by: `town-ctl` validates at
  apply time; the built-in role list changes rarely.
- **`max_concurrent` deferred** → ephemeral roles with `max_instances = 5` can all spawn at once.
  If spawn cost is high (e.g., Claude Code startup), this may cause resource spikes. Accepted for
  MVP; `max_concurrent` added when throttling becomes a real operational concern.

## Migration Plan

1. Write `desired_topology_versions` DDL + `desired_custom_roles` + `desired_rig_custom_roles` DDL
2. Amend ADR-0001 consequences: replace per-row `schema_version` with `desired_topology_versions`
3. Amend ADR-0002 consequences: same amendment + Surveyor pre-flight check note
4. All subsequent `desired_topology` table designs (dgt-9ft) omit per-row `schema_version`
5. `town-ctl` updated to write `desired_topology_versions` first in every apply transaction

## Open Questions

- **`desired_topology_versions` written by Surveyor?** The Surveyor writes `actual_topology`
  tables, not `desired_topology`. Should a parallel `actual_topology_versions` table exist with
  the same pattern? Deferred to dgt-fkm (actual_topology schema design).
- **Role name collisions with built-ins**: should `town-ctl` reject a `[[role]]` with
  `name = "mayor"` or `name = "polecat"` to prevent confusion? Recommend yes — validate at
  apply time that custom role names do not shadow built-in role names.
