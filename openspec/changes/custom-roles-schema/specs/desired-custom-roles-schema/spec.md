## ADDED Requirements

### Requirement: desired_topology_versions table is the single versioning authority
A `desired_topology_versions` table SHALL exist in the Dolt `desired_topology` schema.
It SHALL have columns: `table_name VARCHAR(128) NOT NULL PRIMARY KEY`, `schema_version INT NOT NULL`,
`written_by VARCHAR(128)` (e.g. `"town-ctl/0.2.0"`), `written_at TIMESTAMP NOT NULL DEFAULT
CURRENT_TIMESTAMP`. `town-ctl` SHALL upsert one row per topology table it writes, as the **first**
operation in every apply transaction. No `desired_topology` table SHALL carry a per-row
`schema_version` column.

#### Scenario: town-ctl writes versions table first
- **WHEN** `town-ctl apply` writes any `desired_topology` table
- **THEN** a row in `desired_topology_versions` for that table is written before any data rows in the same transaction

#### Scenario: Surveyor reads versions table before any topology query
- **WHEN** the Surveyor begins a reconcile loop
- **THEN** it reads `desired_topology_versions` first; if any table has an unknown `schema_version`, it hard-fails and files an escalation Bead to Mayor before reading any topology rows

#### Scenario: Schema version mismatch blocks reconcile
- **WHEN** `desired_topology_versions` contains `schema_version = 2` for `desired_custom_roles` and the Surveyor only understands version 1
- **THEN** the Surveyor exits the reconcile loop with a logged error and a Mayor escalation Bead

#### Scenario: written_by records the town-ctl version
- **WHEN** `town-ctl/0.1.0` applies a manifest
- **THEN** `desired_topology_versions` rows written in that transaction have `written_by = "town-ctl/0.1.0"`

### Requirement: desired_custom_roles table stores global role definitions
A `desired_custom_roles` table SHALL exist with the following columns and constraints:

```sql
CREATE TABLE desired_custom_roles (
  name             VARCHAR(128) NOT NULL,
  description      TEXT,
  scope            ENUM('town', 'rig')                                    NOT NULL,
  lifespan         ENUM('ephemeral', 'persistent')                        NOT NULL,
  trigger_type     ENUM('bead_assigned', 'schedule', 'event', 'manual')  NOT NULL,
  trigger_schedule VARCHAR(64),
  trigger_event    VARCHAR(128),
  claude_md_path   VARCHAR(512)  NOT NULL,
  model            VARCHAR(128),
  parent_role      VARCHAR(128)  NOT NULL,
  reports_to       VARCHAR(128),
  max_instances    INT           NOT NULL DEFAULT 1,
  PRIMARY KEY (name),
  CONSTRAINT chk_trigger CHECK (
    (trigger_type = 'schedule' AND trigger_schedule IS NOT NULL) OR
    (trigger_type = 'event'    AND trigger_event    IS NOT NULL) OR
    (trigger_type IN ('bead_assigned', 'manual'))
  )
);
```

`claude_md_path` stores the resolved absolute path — no inline content column exists.
`model` is nullable; a NULL value means the role inherits the appropriate model from `[defaults]`.
`parent_role` is NOT NULL — every custom role must declare a supervision relationship.

#### Scenario: Role row written on apply
- **WHEN** a valid `[[role]]` block exists in `town.toml` and `town-ctl apply` runs
- **THEN** a row is written to `desired_custom_roles` with all fields populated from the manifest

#### Scenario: claude_md_path stores resolved absolute path
- **WHEN** `claude_md = "${GT_HOME}/roles/reviewer/CLAUDE.md"` and `GT_HOME=/home/user/.gt`
- **THEN** `desired_custom_roles.claude_md_path` is `/home/user/.gt/roles/reviewer/CLAUDE.md`

#### Scenario: Trigger CHECK constraint enforced
- **WHEN** a row is inserted with `trigger_type = 'schedule'` and `trigger_schedule = NULL`
- **THEN** Dolt rejects the insert with a CHECK constraint violation

#### Scenario: Role removed on apply when absent from manifest
- **WHEN** a role previously in `desired_custom_roles` is removed from `town.toml` and `town-ctl apply` runs
- **THEN** the row is deleted from `desired_custom_roles` in the same atomic transaction

### Requirement: desired_rig_custom_roles table stores per-rig opt-in
A `desired_rig_custom_roles` table SHALL exist with the following columns and constraints:

```sql
CREATE TABLE desired_rig_custom_roles (
  rig_name  VARCHAR(128) NOT NULL,
  role_name VARCHAR(128) NOT NULL REFERENCES desired_custom_roles(name),
  enabled   BOOLEAN      NOT NULL DEFAULT TRUE,
  PRIMARY KEY (rig_name, role_name)
);
```

Rows are only written for `scope = 'rig'` roles. Town-scoped roles have no rows in this table.
The FK ensures `role_name` references a defined custom role.

#### Scenario: Rig opt-in row written
- **WHEN** `[rig.agents] roles = ["reviewer"]` for rig `backend` and `reviewer` is defined globally
- **THEN** a row `(rig_name='backend', role_name='reviewer', enabled=true)` is written to `desired_rig_custom_roles`

#### Scenario: Town-scoped role has no junction row
- **WHEN** a `[[role]]` has `scope = "town"`
- **THEN** no row is written to `desired_rig_custom_roles` for this role

#### Scenario: FK violation rejected
- **WHEN** `town-ctl` attempts to write `desired_rig_custom_roles` with a `role_name` not present in `desired_custom_roles`
- **THEN** Dolt rejects the insert with a foreign key constraint violation

#### Scenario: Rig opt-in rows removed when rig is removed from manifest
- **WHEN** a `[[rig]]` entry is removed from `town.toml` and `town-ctl apply` runs
- **THEN** all `desired_rig_custom_roles` rows for that `rig_name` are deleted in the same atomic transaction

### Requirement: town-ctl apply is an atomic Dolt transaction for all custom role writes
All writes for a single `town-ctl apply` — `desired_topology_versions`, `desired_custom_roles`,
and `desired_rig_custom_roles` — SHALL occur within a single Dolt transaction. Partial failure
SHALL roll back all writes. Re-running apply after a failure SHALL be safe (idempotent diff).

#### Scenario: Transaction atomicity on failure
- **WHEN** writing `desired_rig_custom_roles` fails mid-transaction
- **THEN** no rows are committed to `desired_topology_versions` or `desired_custom_roles` either

#### Scenario: Idempotent re-apply
- **WHEN** `town-ctl apply` is run twice with an identical manifest
- **THEN** the second apply produces no Dolt diff (no rows inserted, updated, or deleted)
