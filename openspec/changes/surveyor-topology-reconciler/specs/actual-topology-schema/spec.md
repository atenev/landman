## ADDED Requirements

### Requirement: actual_topology comprises three tables mirroring desired_topology
The Dolt `actual_topology` schema SHALL include three tables: `actual_rigs`,
`actual_agent_config`, and `actual_worktrees`. Each table SHALL include `last_seen` (DATETIME)
and `schema_version` (INTEGER) columns to support staleness detection.

#### Scenario: Tables exist before Surveyor starts
- **WHEN** the `actual_topology` tables have been created in Dolt
- **THEN** the Surveyor can read them without error even if they are empty

### Requirement: actual_rigs table schema
`actual_rigs` SHALL have columns: `name` (VARCHAR, primary key), `repo` (TEXT), `branch`
(VARCHAR), `enabled` (BOOLEAN), `status` (VARCHAR: one of running, stopped, error, unknown),
`last_seen` (DATETIME, not null), `schema_version` (INTEGER, not null).

#### Scenario: Deacon writes rig heartbeat
- **WHEN** Deacon performs a health check on a running rig
- **THEN** the rig's `actual_rigs` row is updated with `status = "running"` and
  `last_seen = now()`

### Requirement: actual_agent_config table schema
`actual_agent_config` SHALL have columns: `rig_name` (VARCHAR), `role` (VARCHAR), `pid`
(INTEGER, nullable), `model` (VARCHAR, nullable), `status` (VARCHAR: running, stopped, error),
`last_seen` (DATETIME, not null), `schema_version` (INTEGER, not null).
Primary key is `(rig_name, role)`.

#### Scenario: Polecat writes its own row on spawn
- **WHEN** a Polecat process starts on a rig
- **THEN** it inserts or updates its `actual_agent_config` row with its PID and
  `status = "running"`

#### Scenario: Polecat clears its row on exit
- **WHEN** a Polecat process exits normally
- **THEN** it updates its `actual_agent_config` row with `status = "stopped"` and
  clears its `pid`

### Requirement: actual_worktrees table schema
`actual_worktrees` SHALL have columns: `rig_name` (VARCHAR), `path` (TEXT), `branch`
(VARCHAR), `clean` (BOOLEAN), `last_seen` (DATETIME, not null), `schema_version` (INTEGER).
Primary key is `(rig_name, path)`.

#### Scenario: Worktree row reflects filesystem state
- **WHEN** Deacon scans worktrees for a rig
- **THEN** each worktree's `actual_worktrees` row reflects its current `clean` status and
  `last_seen` is updated

### Requirement: Rows with expired last_seen are treated as unknown state
The Surveyor SHALL treat any `actual_topology` row whose `last_seen` is older than the
configured staleness TTL as `status = "unknown"`. Unknown state SHALL NOT count toward
convergence and SHALL be flagged in the verify loop.

#### Scenario: Stale row treated as unknown
- **WHEN** an `actual_rigs` row has `last_seen` more than TTL seconds ago
- **THEN** the Surveyor treats that rig as status unknown and does not count it as converged

#### Scenario: Unknown state triggers escalation after retries
- **WHEN** a rig remains unknown across all verify retries
- **THEN** the Surveyor escalates to Mayor rather than assuming convergence

### Requirement: actual_topology rows written by Gas Town agents, not the Surveyor
The Surveyor SHALL only READ `actual_topology`. Gas Town agents (Deacon for rigs and
worktrees, Polecats and Dogs for agent_config) SHALL write to these tables as part of
their normal operation. The Surveyor SHALL NOT write to `actual_topology`.

#### Scenario: Surveyor reads but does not write actual state
- **WHEN** the Surveyor runs a verify loop
- **THEN** it queries `actual_topology` via SELECT only; no INSERT/UPDATE is issued by
  the Surveyor process
