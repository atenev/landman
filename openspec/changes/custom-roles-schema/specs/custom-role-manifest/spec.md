## ADDED Requirements

### Requirement: Manifest supports global custom role definitions via [[role]] block
`town.toml` SHALL support zero or more `[[role]]` array-of-table entries at the top level.
Each `[[role]]` entry defines a named agent archetype available for rig opt-in. Role
definitions are global — they are not scoped to a specific rig.

#### Scenario: Single custom role defined
- **WHEN** `town.toml` contains one `[[role]]` entry with valid required fields
- **THEN** `town-ctl` parses it successfully and includes it in the resolved manifest

#### Scenario: Multiple custom roles defined
- **WHEN** `town.toml` contains multiple `[[role]]` entries with distinct `name` values
- **THEN** all roles are parsed and available for per-rig opt-in

#### Scenario: Duplicate role name rejected
- **WHEN** two `[[role]]` entries share the same `name`
- **THEN** `town-ctl` exits non-zero with a validation error identifying the duplicate name

#### Scenario: Custom role name shadows built-in role
- **WHEN** a `[[role]]` entry uses a name matching a built-in role (`mayor`, `polecat`, `witness`, `refinery`, `deacon`, `dog`, `crew`)
- **THEN** `town-ctl` exits non-zero with: `[role.name] "<name>" shadows a built-in Gas Town role`

### Requirement: Each [[role]] entry declares required identity and operational fields
Each `[[role]]` entry SHALL have the following required fields: `name` (string, unique),
`scope` (enum: `town` | `rig`), `lifespan` (enum: `ephemeral` | `persistent`).
Optional fields: `description` (string), `model` (string, inherits from `[defaults]` if unset).

#### Scenario: Minimal valid role
- **WHEN** a `[[role]]` entry contains only `name`, `scope`, and `lifespan`
- **THEN** `town-ctl` parses it successfully with all optional fields at their defaults

#### Scenario: Missing required field
- **WHEN** a `[[role]]` entry omits `scope`
- **THEN** `town-ctl` exits non-zero with: `[[role].<name>] missing required field: scope`

### Requirement: Each [[role]] entry declares a [role.identity] sub-table with claude_md path
Each `[[role]]` entry SHALL have a `[role.identity]` sub-table with a required `claude_md`
field (filesystem path). Inline CLAUDE.md content SHALL NOT be supported. The path SHALL
support `${VAR}` interpolation. `town-ctl` SHALL resolve and validate the path exists at
apply time.

#### Scenario: Valid claude_md path resolved
- **WHEN** `[role.identity] claude_md = "${GT_HOME}/roles/reviewer/CLAUDE.md"` and the path resolves to an existing file
- **THEN** the resolved path is stored in `desired_custom_roles.claude_md_path`

#### Scenario: Non-existent claude_md path rejected
- **WHEN** the resolved `claude_md` path does not exist on the filesystem
- **THEN** `town-ctl` exits non-zero with: `[role.<name>.identity.claude_md] path not found: <resolved-path>`

#### Scenario: Inline content rejected
- **WHEN** `[role.identity]` contains a `content` field instead of `claude_md`
- **THEN** `town-ctl` exits non-zero with: `[role.<name>.identity] inline content not supported; use claude_md path`

### Requirement: Each [[role]] entry declares a [role.trigger] sub-table
Each `[[role]]` entry SHALL have a `[role.trigger]` sub-table with a required `type` field
(enum: `bead_assigned` | `schedule` | `event` | `manual`). When `type = "schedule"`,
`trigger.schedule` (cron string) is required. When `type = "event"`, `trigger.event`
(string event name) is required.

#### Scenario: bead_assigned trigger requires no extra fields
- **WHEN** `[role.trigger] type = "bead_assigned"` with no other trigger fields
- **THEN** `town-ctl` parses it successfully

#### Scenario: schedule trigger requires cron string
- **WHEN** `[role.trigger] type = "schedule"` and `schedule = "0 * * * *"`
- **THEN** `town-ctl` parses it successfully and validates the cron expression

#### Scenario: schedule trigger missing cron string rejected
- **WHEN** `[role.trigger] type = "schedule"` and no `schedule` field is present
- **THEN** `town-ctl` exits non-zero with: `[role.<name>.trigger] schedule required when type = "schedule"`

#### Scenario: event trigger requires event name
- **WHEN** `[role.trigger] type = "event"` and `event = "branch_created"`
- **THEN** `town-ctl` parses it successfully

#### Scenario: event trigger missing event name rejected
- **WHEN** `[role.trigger] type = "event"` and no `event` field is present
- **THEN** `town-ctl` exits non-zero with: `[role.<name>.trigger] event required when type = "event"`

#### Scenario: Invalid cron string rejected
- **WHEN** `[role.trigger] type = "schedule"` and `schedule = "not-a-cron"`
- **THEN** `town-ctl` exits non-zero with a cron validation error identifying the role

### Requirement: Each [[role]] declares a [role.supervision] sub-table
Each `[[role]]` entry SHALL have a `[role.supervision]` sub-table with a required
`parent` field (string). `parent` SHALL be validated at apply time as either a built-in
role name or a defined custom role name. Optional: `reports_to` (string, same validation).

#### Scenario: Valid built-in parent role
- **WHEN** `[role.supervision] parent = "witness"`
- **THEN** `town-ctl` accepts it as a valid supervision reference

#### Scenario: Valid custom role as parent
- **WHEN** `[role.supervision] parent = "reviewer"` and `reviewer` is defined in the same manifest
- **THEN** `town-ctl` accepts it as a valid supervision reference

#### Scenario: Unknown parent role rejected
- **WHEN** `[role.supervision] parent = "nonexistent-role"`
- **THEN** `town-ctl` exits non-zero with: `[role.<name>.supervision.parent] unknown role: "nonexistent-role"`

### Requirement: Each [[role]] declares a [role.resources] sub-table
Each `[[role]]` entry MAY have a `[role.resources]` sub-table. Supported field:
`max_instances` (integer, default 1, minimum 1). Controls the maximum number of concurrent
instances of this role that the Surveyor will spawn.

#### Scenario: max_instances defaults to 1
- **WHEN** `[role.resources]` is omitted
- **THEN** the resolved `max_instances` is 1

#### Scenario: max_instances set explicitly
- **WHEN** `[role.resources] max_instances = 5`
- **THEN** `desired_custom_roles.max_instances` is written as 5

#### Scenario: max_instances below minimum rejected
- **WHEN** `[role.resources] max_instances = 0`
- **THEN** `town-ctl` exits non-zero with: `[role.<name>.resources.max_instances] must be >= 1`

### Requirement: Rig-scoped roles require opt-in; town-scoped roles are always active
A `[[role]]` with `scope = "rig"` SHALL only be active for rigs that list its name in
`[rig.agents].roles`. A `[[role]]` with `scope = "town"` SHALL be active globally whenever
it exists in `desired_custom_roles`, with no rig association required.

#### Scenario: Rig-scoped role inactive without opt-in
- **WHEN** a `[[role]]` has `scope = "rig"` and no `[[rig]]` lists it in `[rig.agents].roles`
- **THEN** no `desired_rig_custom_roles` rows are written for this role

#### Scenario: Town-scoped role active without rig opt-in
- **WHEN** a `[[role]]` has `scope = "town"`
- **THEN** the role is written to `desired_custom_roles` and is active town-wide; no junction row is needed
