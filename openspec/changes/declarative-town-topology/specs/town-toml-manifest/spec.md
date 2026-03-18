## ADDED Requirements

### Requirement: Manifest carries a version field
Every `town.toml` SHALL carry a top-level `version` string field. `town-ctl` SHALL refuse to
parse a manifest whose `version` value it does not recognise, printing the minimum `town-ctl`
version required to process it.

#### Scenario: Known version accepted
- **WHEN** `town.toml` contains `version = "1"` and `town-ctl` supports version 1
- **THEN** parsing proceeds without error

#### Scenario: Unknown version rejected
- **WHEN** `town.toml` contains `version = "2"` and `town-ctl` only supports version 1
- **THEN** `town-ctl` exits non-zero with message: `manifest version "2" requires town-ctl >= X.Y.Z`

### Requirement: Manifest declares town-level metadata
`town.toml` SHALL support a `[town]` table with fields: `name` (string, required), `home`
(path, required), `dolt_port` (integer, default 3306).

#### Scenario: Minimal town block
- **WHEN** `town.toml` contains `[town]` with `name` and `home` only
- **THEN** `town-ctl` parses successfully with `dolt_port` defaulting to 3306

#### Scenario: Missing required field
- **WHEN** `town.toml` contains `[town]` without `name`
- **THEN** `town-ctl` exits non-zero with a validation error identifying the missing field

### Requirement: Manifest declares global defaults
`town.toml` SHALL support a `[defaults]` table with fields: `mayor_model` (string),
`polecat_model` (string), `max_polecats` (integer). These values apply to all rigs unless
overridden at the rig level.

#### Scenario: Rig inherits defaults
- **WHEN** `[defaults]` sets `polecat_model = "claude-sonnet-4-6"` and a rig does not
  set `polecat_model`
- **THEN** the rig's resolved `polecat_model` is `"claude-sonnet-4-6"`

#### Scenario: Rig overrides default
- **WHEN** `[defaults]` sets `max_polecats = 20` and a rig sets `max_polecats = 30`
- **THEN** the rig's resolved `max_polecats` is 30

### Requirement: Manifest declares rigs as an array of tables
`town.toml` SHALL support one or more `[[rig]]` entries. Each rig SHALL have fields: `name`
(string, required, unique), `repo` (path, required), `branch` (string, required),
`enabled` (boolean, default true). Each rig SHALL support a `[rig.agents]` sub-table and
one or more `[[rig.formula]]` entries.

#### Scenario: Multiple rigs declared
- **WHEN** `town.toml` contains two `[[rig]]` entries with distinct `name` values
- **THEN** both rigs are parsed and included in the resolved manifest

#### Scenario: Duplicate rig name rejected
- **WHEN** two `[[rig]]` entries share the same `name`
- **THEN** `town-ctl` exits non-zero with a validation error

### Requirement: Rig agents block controls role presence and capacity
`[rig.agents]` SHALL support boolean fields for each Gas Town role (`mayor`, `witness`,
`refinery`, `deacon`) and integer/string overrides for `max_polecats` and `polecat_model`,
`mayor_claude_md` (path).

#### Scenario: Role disabled for a rig
- **WHEN** `[rig.agents]` sets `witness = false`
- **THEN** the witness role is not included in the rig's `desired_agent_config` rows

#### Scenario: Per-rig model override
- **WHEN** `[rig.agents]` sets `polecat_model = "claude-haiku-4-5-20251001"`
- **THEN** the rig's resolved `polecat_model` is `"claude-haiku-4-5-20251001"` regardless
  of the `[defaults]` value

### Requirement: Rig formula entries declare scheduled workflows
`[[rig.formula]]` entries SHALL have fields: `name` (string, required), `schedule` (cron
string, required).

#### Scenario: Valid cron schedule accepted
- **WHEN** `[[rig.formula]]` contains `schedule = "0 2 * * *"`
- **THEN** the formula is included in `desired_formulas` with the specified schedule

#### Scenario: Invalid cron string rejected
- **WHEN** `[[rig.formula]]` contains an invalid cron expression
- **THEN** `town-ctl` exits non-zero with a validation error identifying the formula

### Requirement: Secrets block declares env-var references, never literal values
`[secrets]` SHALL support fields whose values are env-var interpolation expressions
(`"${VAR_NAME}"`). An optional `file` field points to a gitignored secrets TOML file.
`town-ctl` SHALL fail fast if any referenced env var is unset at apply time.

#### Scenario: Env var resolved
- **WHEN** `anthropic_api_key = "${ANTHROPIC_API_KEY}"` and `ANTHROPIC_API_KEY` is set
- **THEN** the resolved value is the env var's content; it is never written to Dolt

#### Scenario: Unset env var causes fast failure
- **WHEN** `anthropic_api_key = "${ANTHROPIC_API_KEY}"` and `ANTHROPIC_API_KEY` is not set
- **THEN** `town-ctl` exits non-zero before writing anything to Dolt

### Requirement: Town agents block declares process lifecycle
`[town.agents]` SHALL support a `surveyor` boolean field. When `surveyor = true`, `town-ctl`
apply SHALL check whether the Surveyor process is running and launch it if not.

#### Scenario: Surveyor launched on apply
- **WHEN** `[town.agents] surveyor = true` and no Surveyor process is running
- **THEN** `town-ctl apply` starts the Surveyor process and exits zero

#### Scenario: Surveyor already running
- **WHEN** `[town.agents] surveyor = true` and a Surveyor process is already running
- **THEN** `town-ctl apply` takes no action and exits zero

### Requirement: All path fields support env-var interpolation
Any field accepting a filesystem path SHALL support `${VAR}` interpolation resolved at apply
time. Supported variables SHALL include at minimum: `${HOME}`, `${GT_HOME}`, and any env var
present at apply time.

#### Scenario: Path interpolated correctly
- **WHEN** `repo = "${PROJECTS_DIR}/backend"` and `PROJECTS_DIR=/home/user/projects`
- **THEN** the resolved repo path is `/home/user/projects/backend`

### Requirement: Go structs and JSON Schema validated at parse time
`town-ctl` SHALL validate a parsed manifest against the JSON Schema before any other
operation. Validation errors SHALL identify the field path and constraint violated.

#### Scenario: Schema violation reported clearly
- **WHEN** `max_polecats` is set to a negative integer
- **THEN** `town-ctl` exits non-zero with: `[defaults.max_polecats] must be >= 1`
