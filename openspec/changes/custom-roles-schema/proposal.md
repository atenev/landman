## Why

Gas Town's 7 built-in agent roles are hardcoded. There is no mechanism for operators to define new agent archetypes (e.g., a Reviewer, SecurityScanner, or PlanningAgent) without modifying the `gt` binary. Additionally, the `desired_topology` Dolt schema has no version tracking table — the per-row `schema_version` convention in ADR-0001/0002 conflates writer version with schema version, a table-level property that per-row values cannot correctly represent.

## What Changes

- Add `[[role]]` array-of-tables block to `town.toml` for global custom role definitions
- Add `[rig.agents].roles = [...]` per-rig opt-in for custom roles
- Introduce `desired_topology_versions` Dolt table as the single versioning authority for all `desired_topology` tables (replaces per-row `schema_version` convention from ADR-0001/0002) — **BREAKING** change to the schema convention
- Add `desired_custom_roles` Dolt table: stores custom role definitions written by `town-ctl`
- Add `desired_rig_custom_roles` Dolt table: per-rig opt-in junction table
- Amend ADR-0001 and ADR-0002 consequences sections to reflect the versioning strategy change

## Capabilities

### New Capabilities

- `custom-role-manifest`: `[[role]]` TOML block schema — fields, validation rules, trigger types, supervision model, and per-rig opt-in via `[rig.agents].roles`
- `desired-custom-roles-schema`: Dolt DDL for `desired_custom_roles` and `desired_rig_custom_roles` tables, including the `desired_topology_versions` versioning table and its write/read protocol

### Modified Capabilities

- `town-toml-manifest`: `[rig.agents]` sub-table gains a `roles` string array field; `[[role]]` becomes a valid top-level array-of-tables

## Impact

- `town-ctl` binary: must parse `[[role]]` blocks, validate `parent_role` as a known role name, resolve `claude_md_path` at apply time, write `desired_topology_versions` first in every transaction
- Surveyor agent: must read `desired_topology_versions` before any topology query; must diff `desired_custom_roles` + `desired_rig_custom_roles` against `actual_topology` counterparts
- ADR-0001 and ADR-0002: consequences sections amended — per-row `schema_version` convention removed, `desired_topology_versions` table added as the versioning mechanism
- `dgt-9ft` (desired_topology tables): all future tables must omit per-row `schema_version` and rely on `desired_topology_versions` instead
