## ADDED Requirements

### Requirement: desired_topology comprises three tables
The Dolt `desired_topology` schema SHALL include three tables: `desired_rigs`,
`desired_agent_config`, and `desired_formulas`. Each table SHALL include a `schema_version`
integer column.

#### Scenario: Tables exist after first apply
- **WHEN** `town-ctl apply` runs for the first time on an empty Dolt database
- **THEN** all three tables are created with the correct columns before any rows are written

### Requirement: desired_rigs table schema
`desired_rigs` SHALL have columns: `name` (VARCHAR, primary key), `repo` (TEXT, not null),
`branch` (VARCHAR, not null), `enabled` (BOOLEAN, not null, default true),
`schema_version` (INTEGER, not null).

#### Scenario: Rig row written correctly
- **WHEN** `town-ctl apply` processes a `[[rig]]` with name=`backend`, repo=`/path/to/repo`,
  branch=`main`, enabled=`true`
- **THEN** `desired_rigs` contains exactly one row matching those values

### Requirement: desired_agent_config table schema
`desired_agent_config` SHALL have columns: `rig_name` (VARCHAR, foreign key → desired_rigs.name),
`role` (VARCHAR, one of: mayor, witness, refinery, deacon, polecat), `enabled` (BOOLEAN),
`model` (VARCHAR, nullable), `max_count` (INTEGER, nullable), `claude_md_path` (TEXT, nullable),
`schema_version` (INTEGER, not null). Primary key is `(rig_name, role)`.

#### Scenario: Disabled role not written
- **WHEN** `[rig.agents]` sets `witness = false`
- **THEN** no row with `role = "witness"` for that rig exists in `desired_agent_config`

#### Scenario: Model override written
- **WHEN** `[rig.agents]` sets `polecat_model = "claude-haiku-4-5-20251001"`
- **THEN** the row with `role = "polecat"` for that rig has `model = "claude-haiku-4-5-20251001"`

### Requirement: desired_formulas table schema
`desired_formulas` SHALL have columns: `rig_name` (VARCHAR, foreign key → desired_rigs.name),
`name` (VARCHAR), `schedule` (VARCHAR, cron format), `schema_version` (INTEGER, not null).
Primary key is `(rig_name, name)`.

#### Scenario: Formula row written correctly
- **WHEN** `[[rig.formula]]` has `name = "nightly-tests"` and `schedule = "0 2 * * *"`
- **THEN** `desired_formulas` contains a row with those values for the parent rig

### Requirement: schema_version enforces compatibility
Every row written by `town-ctl` SHALL carry the `schema_version` that `town-ctl` understands.
Any reader (Surveyor) that encounters an unknown `schema_version` SHALL refuse to act on
those rows and SHALL file an escalation.

#### Scenario: Mismatched schema version detected by reader
- **WHEN** `desired_rigs` rows carry `schema_version = 2` and the Surveyor only knows version 1
- **THEN** the Surveyor does not process those rows and files an escalation Bead to Mayor

### Requirement: Each apply is a single Dolt commit
`town-ctl apply` SHALL wrap all row writes (insertions, updates, deletions across all three
tables) in a single Dolt transaction committed as one Dolt commit. The commit message SHALL
include: the manifest path, the `town-ctl` version, and a summary of operations performed.

#### Scenario: Apply commit is queryable in Dolt log
- **WHEN** `town-ctl apply` runs successfully
- **THEN** `dolt log` shows one new commit with message containing the manifest path and
  operation summary

### Requirement: Desired state is the full manifest, deletions are explicit
When a rig present in Dolt `desired_rigs` is absent from the current `town.toml`, `town-ctl`
apply SHALL delete that rig's rows from all three tables. Desired state is always the full
resolved manifest — there is no append-only mode.

#### Scenario: Removed rig deleted from Dolt
- **WHEN** a rig named `frontend` exists in `desired_rigs` and the new `town.toml` does not
  contain a `[[rig]]` with `name = "frontend"`
- **THEN** `town-ctl apply` deletes all rows for `frontend` across all desired tables
