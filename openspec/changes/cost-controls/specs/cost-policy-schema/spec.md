## ADDED Requirements

### Requirement: `desired_cost_policy` table exists with correct schema
The `desired_cost_policy` Dolt table SHALL have columns: `rig_name VARCHAR(128) NOT NULL
PRIMARY KEY`, `budget_type ENUM('usd','messages','tokens') NOT NULL`, `daily_budget
DECIMAL(16,4) NOT NULL`, `warn_at_pct TINYINT NOT NULL DEFAULT 80`.

#### Scenario: Table created by migration
- **WHEN** the DDL migration is applied to a Dolt instance
- **THEN** `desired_cost_policy` exists with the correct column types and constraints

#### Scenario: warn_at_pct defaults to 80
- **WHEN** a row is inserted without specifying `warn_at_pct`
- **THEN** `warn_at_pct` is 80

### Requirement: `desired_topology_versions` row written for `desired_cost_policy`
`town-ctl` SHALL upsert a `desired_topology_versions` row with `table_name =
'desired_cost_policy'` as the first SQL statement in every apply transaction that touches
`desired_cost_policy`. This is the ADR-0003 contract.

#### Scenario: Version row written on first apply with cost policy
- **WHEN** `town-ctl apply` is run with a manifest containing `[rig.cost]`
- **THEN** `desired_topology_versions` contains a row for `desired_cost_policy` with
  `schema_version = 1` and the correct `written_by` value

#### Scenario: Version row written even when only defaults.cost is set
- **WHEN** `town-ctl apply` is run with `[defaults.cost]` but no per-rig `[rig.cost]`
- **THEN** `desired_topology_versions` row for `desired_cost_policy` is written

### Requirement: Rig with active policy has exactly one row in `desired_cost_policy`
After `town-ctl apply`, each rig whose resolved cost policy (from `[rig.cost]` or
inherited from `[defaults.cost]`) is active SHALL have exactly one row in
`desired_cost_policy`.

#### Scenario: Rig with explicit [rig.cost] block
- **WHEN** `town.toml` contains `[[rig]] name="backend"` with `[rig.cost]
  daily_budget_usd = 50.0`
- **THEN** `desired_cost_policy` contains one row with `rig_name='backend'`,
  `budget_type='usd'`, `daily_budget=50.0`

#### Scenario: Rig inheriting from [defaults.cost]
- **WHEN** `[defaults.cost] daily_budget_messages = 500` is set and a rig has no
  `[rig.cost]` block
- **THEN** `desired_cost_policy` contains a row for that rig with `budget_type='messages'`,
  `daily_budget=500`

#### Scenario: Rig overrides defaults with different budget type
- **WHEN** `[defaults.cost]` sets `daily_budget_usd = 200` and `[rig.cost]` sets
  `daily_budget_messages = 300`
- **THEN** the rig row has `budget_type='messages'`, `daily_budget=300` (not USD)

### Requirement: Unrestricted rig has no row in `desired_cost_policy`
A rig with no `[rig.cost]` block and no `[defaults.cost]` in `town.toml` SHALL have NO
row in `desired_cost_policy` after `town-ctl apply`.

#### Scenario: No cost blocks in manifest
- **WHEN** `town.toml` has no `[rig.cost]` and no `[defaults.cost]`
- **THEN** `desired_cost_policy` contains zero rows after apply

#### Scenario: Some rigs have policy, some do not
- **WHEN** `[defaults.cost]` is absent, rig A has `[rig.cost]`, rig B does not
- **THEN** `desired_cost_policy` has exactly one row (for rig A); rig B has no row

### Requirement: Removed rig's policy row is deleted on apply
When a rig is removed from `town.toml`, `town-ctl apply` SHALL DELETE the corresponding
`desired_cost_policy` row within the same atomic transaction.

#### Scenario: Rig removed from manifest
- **WHEN** a rig with a `[rig.cost]` block is removed from `town.toml` and `town-ctl apply`
  is run
- **THEN** the `desired_cost_policy` row for that rig is deleted

### Requirement: `[rig.cost]` validates mutual exclusion of budget fields
`town-ctl` SHALL reject a manifest where more than one of `daily_budget_usd`,
`daily_budget_messages`, `daily_budget_tokens` is set in the same `[rig.cost]` or
`[defaults.cost]` block.

#### Scenario: Two budget fields set
- **WHEN** `[rig.cost]` sets both `daily_budget_usd = 50.0` and `daily_budget_messages = 500`
- **THEN** `town-ctl` exits non-zero with: `[rig.backend.cost] sets both daily_budget_usd
  and daily_budget_messages. Exactly one budget type may be set per cost policy block.`

#### Scenario: Zero budget fields with cost block present
- **WHEN** `[rig.cost]` contains only `warn_at_pct = 75` and no budget field
- **THEN** `town-ctl` exits non-zero with: `[rig.backend.cost] declares no budget.
  At least one of daily_budget_usd, daily_budget_messages, daily_budget_tokens is required.`

### Requirement: `warn_at_pct` is validated in range 1–99
`town-ctl` SHALL reject `warn_at_pct` values outside the range [1, 99].

#### Scenario: warn_at_pct = 0
- **WHEN** `warn_at_pct = 0` is set
- **THEN** `town-ctl` exits non-zero with a validation error

#### Scenario: warn_at_pct = 100
- **WHEN** `warn_at_pct = 100` is set
- **THEN** `town-ctl` exits non-zero with a validation error (100% warn is equivalent
  to hard cap — use the hard cap)

### Requirement: `daily_budget_usd` must be positive
`town-ctl` SHALL reject `daily_budget_usd` values less than or equal to 0.

#### Scenario: Zero USD budget
- **WHEN** `daily_budget_usd = 0.0`
- **THEN** `town-ctl` exits non-zero with a validation error
