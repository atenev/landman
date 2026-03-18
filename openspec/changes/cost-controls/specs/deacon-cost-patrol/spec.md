## ADDED Requirements

### Requirement: Deacon runs cost patrol every 5 minutes (configurable)
Deacon SHALL run a cost patrol cycle on a configurable interval (default: 300 seconds).
The interval is configurable via `[town.cost] patrol_interval_seconds` in `town.toml`.

#### Scenario: Default patrol interval
- **WHEN** `town.toml` has no `[town.cost]` block
- **THEN** Deacon runs cost patrol every 300 seconds

#### Scenario: Custom patrol interval
- **WHEN** `[town.cost] patrol_interval_seconds = 60` is set
- **THEN** Deacon runs cost patrol every 60 seconds

### Requirement: Cost patrol query covers all rigs in desired_cost_policy
Each patrol cycle SHALL query `desired_cost_policy` LEFT JOIN `cost_ledger_24h` and
compute `pct_used` for each rig using the appropriate column per `budget_type`.

#### Scenario: Query returns correct pct_used for usd budget type
- **WHEN** `desired_cost_policy` has `budget_type='usd'`, `daily_budget=50.0` and
  `cost_ledger_24h` reports `spend_usd=40.0` for that rig
- **THEN** `pct_used = 80.0`

#### Scenario: Query returns correct pct_used for messages budget type
- **WHEN** `desired_cost_policy` has `budget_type='messages'`, `daily_budget=500` and
  `cost_ledger_24h` reports `spend_messages=450`
- **THEN** `pct_used = 90.0`

#### Scenario: Rig with no spend yet (NULL from LEFT JOIN)
- **WHEN** `desired_cost_policy` has a row for rig "new-rig" but `cost_ledger_24h`
  has no row for it (no Polecats have exited yet today)
- **THEN** `pct_used = 0` (NULL treated as 0 spend)

### Requirement: Unrestricted rigs are never patrolled
Rigs absent from `desired_cost_policy` SHALL NOT appear in the patrol query result and
SHALL receive no cost-related Beads from Deacon.

#### Scenario: Rig with no policy row
- **WHEN** rig "unrestricted-rig" has no row in `desired_cost_policy`
- **THEN** no Bead is filed for "unrestricted-rig" during the cost patrol

### Requirement: Hard cap breach (pct_used >= 100) triggers drain Bead
When a rig's `pct_used >= 100`, Deacon SHALL file a Bead with priority=0, type=task,
tagged `cost-cap`, directed at the drain execution path. The Bead description SHALL
include: rig name, current spend, daily budget, budget type, and pct_used.

#### Scenario: Hard cap breach
- **WHEN** rig "backend" has `pct_used = 103.5`
- **THEN** Deacon files a Bead: `title="COST CAP: drain rig backend"`, `priority=0`,
  tag `cost-cap`, description includes spend details

### Requirement: Soft warning (warn_at_pct <= pct_used < 100) triggers Mayor Bead
When a rig's `pct_used >= warn_at_pct` but `< 100`, Deacon SHALL file a Bead with
priority=1, assigned to Mayor, tagged `cost-warning`. The Bead SHALL include: rig name,
current spend, daily budget, pct_used, and projected time to hard cap at current burn rate.

#### Scenario: Soft warning threshold
- **WHEN** rig "backend" has `warn_at_pct=80` and `pct_used=85.0`
- **THEN** Deacon files a Bead: `title="COST WARNING: rig backend at 85%"`, `priority=1`,
  assigned to Mayor, tag `cost-warning`

### Requirement: Duplicate Bead prevention within patrol window
Before filing any cost Bead, Deacon SHALL check for an existing open Bead with the same
rig name and tag (`cost-cap` or `cost-warning`). If one exists, no new Bead is filed.

#### Scenario: Repeated patrol cycle while cap remains breached
- **WHEN** rig "backend" has an open `cost-cap` Bead and the next patrol cycle still
  finds `pct_used >= 100`
- **THEN** no additional Bead is filed (the existing open Bead is sufficient)

#### Scenario: Warning Bead resolved, cap then breached
- **WHEN** an open `cost-warning` Bead for rig "backend" is closed, and the next patrol
  finds `pct_used >= 100`
- **THEN** Deacon files a new `cost-cap` Bead (the warning Bead closure does not prevent
  a new cap Bead from being filed)

### Requirement: Drain Bead format follows Surveyor operation Bead conventions
Cost-cap drain Beads filed by Deacon SHALL follow the same operation Bead format as
Surveyor drain Beads (ADR-0002 Decision 7), including: reconcile context tag, rig name,
instruction to drain all Polecats gracefully and block until count reaches 0.

#### Scenario: Drain Bead content
- **WHEN** a drain Bead is filed for rig "backend"
- **THEN** the Bead description contains: "Drain all Polecats on rig backend. Block until
  Polecat count reaches 0. Reason: cost hard cap. Tag: cost-cap."
