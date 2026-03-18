## MODIFIED Requirements

### Requirement: Rig agents block controls role presence and capacity
`[rig.agents]` SHALL support boolean fields for each Gas Town role (`mayor`, `witness`,
`refinery`, `deacon`) and integer/string overrides for `max_polecats` and `polecat_model`,
`mayor_claude_md` (path). `[rig.agents]` SHALL additionally support a `roles` field: an array
of strings, each naming a globally-defined custom role (`[[role]]` entry) to enable for this rig.
Custom role names not present in the global `[[role]]` definitions SHALL be a validation error.

#### Scenario: Role disabled for a rig
- **WHEN** `[rig.agents]` sets `witness = false`
- **THEN** the witness role is not included in the rig's `desired_agent_config` rows

#### Scenario: Per-rig model override
- **WHEN** `[rig.agents]` sets `polecat_model = "claude-haiku-4-5-20251001"`
- **THEN** the rig's resolved `polecat_model` is `"claude-haiku-4-5-20251001"` regardless of the `[defaults]` value

#### Scenario: Custom roles opted in for a rig
- **WHEN** `[rig.agents] roles = ["reviewer", "security-scanner"]` and both names are defined as `[[role]]` entries
- **THEN** rows for both roles are written to `desired_rig_custom_roles` for this rig

#### Scenario: Empty roles array is valid
- **WHEN** `[rig.agents] roles = []` or `roles` field is omitted
- **THEN** no `desired_rig_custom_roles` rows are written for this rig; no error

#### Scenario: Unknown custom role name in roles array rejected
- **WHEN** `[rig.agents] roles = ["nonexistent-role"]` and no `[[role]]` with that name exists
- **THEN** `town-ctl` exits non-zero with: `[rig.<rig-name>.agents.roles] unknown role: "nonexistent-role"`

#### Scenario: Town-scoped role in rig roles array rejected
- **WHEN** `[rig.agents] roles = ["planner"]` and `planner` has `scope = "town"`
- **THEN** `town-ctl` exits non-zero with: `[rig.<rig-name>.agents.roles] role "planner" is town-scoped and cannot be opted in per rig`
