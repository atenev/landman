## 1. Dolt Schema — desired_topology

- [ ] 1.1 Design and document `desired_rigs` table schema (name, repo, branch, enabled, schema_version) [dgt-9ft]
- [ ] 1.2 Design and document `desired_agent_config` table schema (rig_name, role, enabled, model, max_count, claude_md_path, schema_version) [dgt-9ft]
- [ ] 1.3 Design and document `desired_formulas` table schema (rig_name, name, schedule, schema_version) [dgt-9ft]
- [ ] 1.4 Write Dolt SQL migration files for all three tables [dgt-ao3]
- [ ] 1.5 Validate schema supports full diff and atomic apply semantics [dgt-9ft]

## 2. Manifest Format — town.toml

- [ ] 2.1 Define Go structs for TownConfig, Defaults, Rig, RigAgents, Formula, Secrets, TownAgents [dgt-3gj, dgt-4gp]
- [ ] 2.2 Add `go-validator` struct tags for all required fields and constraints [dgt-4gp]
- [ ] 2.3 Generate JSON Schema from Go structs (or author manually) [dgt-4gp]
- [ ] 2.4 Implement env-var interpolation for all path and secrets fields [dgt-apu]
- [ ] 2.5 Write three annotated canonical examples: single-rig minimal, multi-rig production, env overlay [dgt-wpk]
- [ ] 2.6 Add `[town.agents]` block to Go structs and JSON Schema [dgt-q8q]
- [ ] 2.7 Reserve extension slots `[rig.cost]` and `[[rig.role]]` in schema as documented no-ops [dgt-su1]

## 3. Manifest Includes and Overlay Merge Semantics

- [ ] 3.1 Define and document explicit merge rules: array append, scalar base-wins, env overlay last-wins [dgt-cfi]
- [ ] 3.2 Implement glob resolution relative to `town.toml` directory [dgt-apu]
- [ ] 3.3 Implement duplicate rig name detection across base + included files (hard error) [dgt-apu]
- [ ] 3.4 Implement `--env` overlay file loading and application [dgt-apu]
- [ ] 3.5 Write unit tests covering all merge rule edge cases [dgt-vht]

## 4. town-ctl Binary

- [ ] 4.1 Scaffold `town-ctl` Go binary (cmd/town-ctl) with cobra CLI [dgt-i36]
- [ ] 4.2 Implement `apply` command: parse → validate → resolve includes → resolve secrets → diff → write [dgt-i36]
- [ ] 4.3 Implement Dolt connection and atomic transaction write [dgt-apu]
- [ ] 4.4 Implement diff logic: compare resolved manifest against current `desired_topology` rows [dgt-apu]
- [ ] 4.5 Implement `--dry-run` flag: print structured plan (add/update/remove per row) without writing [dgt-apu]
- [ ] 4.6 Implement `--env` flag: load and apply overlay before diff [dgt-apu]
- [ ] 4.7 Implement process-liveness check and launch for `[town.agents]` entries [dgt-apu]
- [ ] 4.8 Implement `version` field enforcement: reject unknown manifest versions with clear error [dgt-apu]
- [ ] 4.9 Implement `desired_topology_versions` table write (first in every transaction, ADR-0003) [dgt-lx5]
- [ ] 4.10 Implement secrets resolution: env-var interpolation, secrets file loading, fast-fail on missing [dgt-apu]

## 5. Validation and Error Handling

- [ ] 5.1 Wire JSON Schema validation into parse step; surface field path + constraint in error messages [dgt-apu]
- [ ] 5.2 Ensure all failures exit non-zero with human-readable stderr output [dgt-apu]
- [ ] 5.3 Write integration tests: valid manifest apply, dry-run, unknown version, missing secret, Dolt failure [dgt-vht]

## 6. Documentation

- [ ] 6.1 Write `town-ctl --help` text documenting all flags and resolution order [dgt-apu]
- [ ] 6.2 Finalize and commit annotated `town.toml` examples [dgt-wpk]
