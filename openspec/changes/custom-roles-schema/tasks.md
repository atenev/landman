## 1. Dolt DDL

- [ ] 1.1 Write `desired_topology_versions` CREATE TABLE DDL with columns: `table_name PK`, `schema_version`, `written_by`, `written_at` [dgt-uxa]
- [ ] 1.2 Write `desired_custom_roles` CREATE TABLE DDL with all columns, PK on `name`, and `chk_trigger` CHECK constraint [dgt-uxa]
- [ ] 1.3 Write `desired_rig_custom_roles` CREATE TABLE DDL with FK to `desired_custom_roles(name)` and composite PK [dgt-uxa]
- [ ] 1.4 Write Dolt SQL migration file (up migration) combining all three tables in dependency order [dgt-40u]
- [ ] 1.5 Write Dolt SQL migration file (down migration / rollback) dropping tables in reverse dependency order [dgt-40u]

## 2. town-ctl: [[role]] Parsing and Validation

- [ ] 2.1 Add `Role`, `RoleIdentity`, `RoleTrigger`, `RoleSupervision`, `RoleResources` Go structs to manifest types [dgt-lai]
- [ ] 2.2 Add `Roles []string` field to `RigAgents` Go struct [dgt-lai]
- [ ] 2.3 Add JSON Schema definitions for `[[role]]` array-of-tables and `[rig.agents].roles` string array [dgt-lai]
- [ ] 2.4 Implement validation: duplicate role names → hard error [dgt-69d]
- [ ] 2.5 Implement validation: role name shadows built-in role → hard error [dgt-69d]
- [ ] 2.6 Implement validation: `claude_md` path resolves and exists at apply time → hard error if not [dgt-69d]
- [ ] 2.7 Implement validation: trigger field cross-constraints (schedule requires cron, event requires event name) [dgt-69d]
- [ ] 2.8 Implement validation: cron string syntax check for `trigger.schedule` [dgt-69d]
- [ ] 2.9 Implement validation: `parent_role` and `reports_to` are known role names (built-in list + parsed custom roles) [dgt-69d]
- [ ] 2.10 Implement validation: `[rig.agents].roles` entries reference defined global role names → hard error if unknown [dgt-69d]
- [ ] 2.11 Implement validation: town-scoped roles rejected in `[rig.agents].roles` → hard error [dgt-69d]

## 3. town-ctl: Dolt Write Protocol

- [ ] 3.1 Implement `desired_topology_versions` upsert as the first operation in every apply transaction [dgt-lx5]
- [ ] 3.2 Implement diff logic: compare resolved `[[role]]` definitions against current `desired_custom_roles` rows [dgt-ytm]
- [ ] 3.3 Implement diff logic: compare resolved rig opt-ins against current `desired_rig_custom_roles` rows [dgt-ytm]
- [ ] 3.4 Implement atomic Dolt transaction: versions → custom_roles → rig_custom_roles in dependency order [dgt-ytm]
- [ ] 3.5 Implement idempotent apply: no-op if diff is empty (no rows inserted, updated, or deleted) [dgt-ytm]
- [ ] 3.6 Implement `--dry-run` output for custom role diffs (added/removed/modified roles and rig opt-ins) [dgt-ytm]

## 4. ADR Amendments

- [ ] 4.1 Amend ADR-0001 consequences section: remove per-row `schema_version` convention; add `desired_topology_versions` table description and write/read protocol [dgt-lx5]
- [ ] 4.2 Amend ADR-0002 consequences section: same versioning amendment; add note that Surveyor reads `desired_topology_versions` as pre-flight before any topology query [dgt-lx5]
- [ ] 4.3 Update ADR-0001 reference manifest skeleton: remove `schema_version` from any implied table design notes; add comment referencing `desired_topology_versions` [dgt-lx5]

## 5. Annotated Examples

- [ ] 5.1 Add `[[role]]` reviewer example to annotated `town.toml` (bead_assigned trigger, rig-scoped, ephemeral, parent=witness) [dgt-rlf]
- [ ] 5.2 Add `[[role]]` security-scanner example (event trigger, rig-scoped, ephemeral, parent=deacon) [dgt-rlf]
- [ ] 5.3 Add `[[role]]` planner example (bead_assigned trigger, town-scoped, persistent, parent=mayor) [dgt-rlf]
- [ ] 5.4 Show per-rig opt-in: `[rig.agents] roles = ["reviewer", "security-scanner"]` in backend rig example [dgt-rlf]

## 6. Tests

- [ ] 6.1 Unit tests: `[[role]]` parser — valid inputs, all required-field missing cases, duplicate name, built-in name shadow [dgt-bvf]
- [ ] 6.2 Unit tests: trigger validation — all four trigger types, missing schedule/event field errors [dgt-bvf]
- [ ] 6.3 Unit tests: `parent_role` validation — built-in names accepted, custom names accepted, unknown name rejected [dgt-bvf]
- [ ] 6.4 Unit tests: `[rig.agents].roles` validation — unknown role rejected, town-scoped role rejected [dgt-bvf]
- [ ] 6.5 Unit tests: Dolt diff logic — add role, remove role, modify role, add rig opt-in, remove rig opt-in [dgt-bvf]
- [ ] 6.6 Integration test: full apply with custom roles → verify `desired_topology_versions` + `desired_custom_roles` + `desired_rig_custom_roles` rows [dgt-6jy]
- [ ] 6.7 Integration test: idempotent re-apply produces no Dolt diff [dgt-6jy]
- [ ] 6.8 Integration test: `--dry-run` with custom role changes prints structured diff, writes nothing [dgt-6jy]
