# `town.toml` Reference

Complete key-by-key reference for the `town.toml` declarative manifest format.

> **See also**: [Declarative Overview](declarative-overview.md) for conceptual background.

---

## Environment variable interpolation

All string-type fields that accept file-system paths, and all fields in the `[secrets]`
block, support `${VAR}` interpolation. `town-ctl` substitutes each expression with the
corresponding environment variable value at apply time. Resolution fails fast: if any
`${VAR}` is unresolvable after checking both the environment and the optional secrets file,
`town-ctl` exits non-zero before writing to Dolt.

Fields that support `${VAR}` interpolation are marked **env-interpolated** in the tables
below.

---

## Top-level fields

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `version` | string | yes | — | Manifest schema version. Currently `"1"`. `town-ctl` refuses to apply unknown versions. |
| `includes` | string[] | no | `[]` | Glob patterns (relative to `town.toml`) of additional TOML fragments to merge. `[[rig]]` and `[[role]]` arrays are appended; scalar top-level fields in included files are ignored. Duplicate rig or role names across files are a hard error. **env-interpolated** |

---

## `[town]`

Town-level identity and connection settings.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `town.name` | string | yes | — | Logical name for this Gas Town instance. Used as a label in Dolt commit messages and Surveyor logs. |
| `town.home` | string | yes | — | Path to the Gas Town home directory (equivalent to `$GT_HOME`). **env-interpolated** |
| `town.dolt_port` | integer | no | `3306` | TCP port on which the Dolt server listens. |

### `[town.agents]`

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `town.agents.surveyor` | boolean | no | `false` | Whether to launch the Surveyor topology-reconciler process after a successful apply. If `true` and the Surveyor is not already running, `town-ctl` starts it as a detached process. |

---

## `[defaults]`

Town-wide defaults inherited by every rig that does not declare its own value. All keys
are optional; absent keys leave Gas Town's built-in defaults in place.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `defaults.mayor_model` | string | no | Gas Town default | Model ID for Mayor agents across all rigs. Example: `"claude-opus-4-6"` |
| `defaults.polecat_model` | string | no | Gas Town default | Model ID for Polecat workers across all rigs. Example: `"claude-sonnet-4-6"` |
| `defaults.max_polecats` | integer | no | Gas Town default | Maximum concurrent Polecats across all rigs. Per-rig `[rig.agents].max_polecats` overrides this. |

### `[defaults.cost]`

Optional town-wide cost safety net. Absent = all rigs are unrestricted by default.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `defaults.cost.daily_budget_usd` | float | no | — | USD spend cap per rig per day. Use for API-billing (pay-per-token) accounts. Mutually exclusive with `daily_budget_messages` and `daily_budget_tokens`. |
| `defaults.cost.daily_budget_messages` | integer | no | — | Message-count cap per rig per day. Use for subscription accounts (Claude Max). Mutually exclusive with `daily_budget_usd`. |
| `defaults.cost.daily_budget_tokens` | integer | no | — | Token-count cap per rig per day. Use for subscription accounts. Mutually exclusive with `daily_budget_usd`. |
| `defaults.cost.warn_at_pct` | integer | no | `80` | Percentage of budget at which Deacon emits a warning. Range: `1`–`99`. |

---

## `[secrets]`

Secret values referenced as `${VAR}` expressions. Resolved at apply time; never written to
Dolt or logs. The `town.toml` file is safe to commit to git when secrets use `${VAR}`
references.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `secrets.anthropic_api_key` | string | no | — | Anthropic API key. **env-interpolated** |
| `secrets.github_token` | string | no | — | GitHub personal access token. **env-interpolated** |
| `secrets.file` | string | no | — | Path to an optional gitignored TOML file (`map[string]string`) from which unresolved `${VAR}` references are looked up after the environment pass. **env-interpolated** |

Additional keys are supported in `[secrets]` for any secret your CLAUDE.md files
reference. All string values are **env-interpolated**.

---

## `[[role]]`

Declares a custom agent role. Defined once globally; rigs opt in via
`[rig.agents].roles`. Multiple `[[role]]` tables may appear.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `role.name` | string | yes | — | Unique identifier for this role. Referenced by rigs in `[rig.agents].roles`. |
| `role.scope` | string | yes | — | Where the role can be used. Currently only `"rig"` is supported. |

### `[role.identity]`

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `role.identity.claude_md` | string | yes | — | Path to the CLAUDE.md file that defines this agent's identity and instructions. Inline content is not supported. **env-interpolated** |

### `[role.trigger]`

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `role.trigger.type` | string | yes | — | What event activates this role. One of: `"bead_assigned"`, `"schedule"`, `"event"`, `"manual"`. |

### `[role.supervision]`

Mandatory. Every custom role must have a supervision relationship.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `role.supervision.parent` | string | yes | — | Built-in Gas Town role that supervises this custom role. One of: `"mayor"`, `"witness"`, `"refinery"`, `"deacon"`. |
| `role.supervision.reports_to` | string | yes | — | Built-in role to which this role reports its output. One of: `"mayor"`, `"witness"`, `"refinery"`, `"deacon"`. |

### `[role.resources]`

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `role.resources.max_instances` | integer | no | `1` | Maximum number of concurrent instances of this role per rig. |

---

## `[[rig]]`

Declares one Gas Town rig. Multiple `[[rig]]` tables may appear. Rigs may also be defined
in files matched by `includes`.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `rig.name` | string | yes | — | Unique rig identifier. Used as the primary key in `desired_rigs`. |
| `rig.repo` | string | yes | — | Absolute path to the git repository this rig operates on. **env-interpolated** |
| `rig.branch` | string | no | `"main"` | Git branch the rig tracks. |
| `rig.enabled` | boolean | no | `true` | Whether the rig is active. Set to `false` to park a rig without removing its definition. |

### `[rig.agents]`

Per-rig agent configuration. All keys are optional; absent keys inherit from `[defaults]`.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `rig.agents.mayor` | boolean | no | `true` | Enable the Mayor agent for this rig. |
| `rig.agents.witness` | boolean | no | `true` | Enable the Witness agent for this rig. |
| `rig.agents.refinery` | boolean | no | `true` | Enable the Refinery agent for this rig. |
| `rig.agents.deacon` | boolean | no | `true` | Enable the Deacon agent for this rig. |
| `rig.agents.max_polecats` | integer | no | `defaults.max_polecats` | Maximum concurrent Polecats for this rig. Overrides the town-wide default. |
| `rig.agents.polecat_model` | string | no | `defaults.polecat_model` | Model ID for Polecats on this rig. Overrides the town-wide default. |
| `rig.agents.roles` | string[] | no | `[]` | Names of custom `[[role]]` blocks to enable on this rig. Each name must match a globally defined `[[role]].name`. |

### `[rig.cost]`

Per-rig cost policy. Overrides `[defaults.cost]` for this rig. Absent = inherits
`[defaults.cost]`; if that is also absent, the rig is unrestricted.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `rig.cost.daily_budget_usd` | float | no | — | USD spend cap per day for this rig. Mutually exclusive with `daily_budget_messages` and `daily_budget_tokens`. |
| `rig.cost.daily_budget_messages` | integer | no | — | Message-count cap per day for this rig. Mutually exclusive with `daily_budget_usd`. |
| `rig.cost.daily_budget_tokens` | integer | no | — | Token-count cap per day for this rig. Mutually exclusive with `daily_budget_usd`. |
| `rig.cost.warn_at_pct` | integer | no | `80` | Percentage of this rig's budget at which Deacon emits a warning. Range: `1`–`99`. |

### `[[rig.formula]]`

Declares a scheduled agent workflow for this rig. Multiple `[[rig.formula]]` tables may
appear under a single `[[rig]]`.

| Key | Type | Required | Default | Description |
|---|---|---|---|---|
| `rig.formula.name` | string | yes | — | Unique formula name within this rig. |
| `rig.formula.schedule` | string | yes | — | Cron expression defining when the formula runs. Example: `"0 2 * * *"` (daily at 02:00). |

---

## Complete annotated example

The following example covers every feature of the manifest format.

```toml
# town.toml
# Manifest schema version. Currently "1".
version = "1"

# ─── Town ────────────────────────────────────────────────────────────────────

[town]
name      = "acme-town"
home      = "${GT_HOME}"      # ${VAR} resolved at apply time
dolt_port = 3306

  [town.agents]
  # Start the Surveyor topology-reconciler process after each apply.
  surveyor = true

# ─── Defaults ────────────────────────────────────────────────────────────────

[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20

  [defaults.cost]
  # Town-wide safety net for all rigs that don't declare their own [rig.cost].
  # Absent = unrestricted by default.
  daily_budget_usd = 200.0
  warn_at_pct      = 80

# ─── Secrets ─────────────────────────────────────────────────────────────────

[secrets]
# ${VAR} is substituted at apply time. Never written to Dolt.
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"
# Optional: load additional secrets from a gitignored file.
# file = "${HOME}/.gt/secrets.toml"

# ─── Includes ────────────────────────────────────────────────────────────────

# Load per-rig TOML fragments. [[rig]] arrays are merged; duplicates are an error.
includes = ["./rigs/*.toml"]

# ─── Custom roles ────────────────────────────────────────────────────────────

[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  # Path to CLAUDE.md defining this agent's identity. Inline content not allowed.
  claude_md = "${GT_HOME}/roles/reviewer/CLAUDE.md"

  [role.trigger]
  # Activate when a Bead is assigned to this role.
  type = "bead_assigned"

  [role.supervision]
  # Every custom role must have a built-in supervisor.
  parent     = "witness"
  reports_to = "mayor"

  [role.resources]
  max_instances = 3

# ─── Rigs ─────────────────────────────────────────────────────────────────────

[[rig]]
name    = "backend"
repo    = "${PROJECTS_DIR}/backend"  # env-interpolated path
branch  = "main"
enabled = true

  [rig.agents]
  mayor        = true
  witness      = true
  refinery     = true
  deacon       = true
  max_polecats  = 30
  polecat_model = "claude-haiku-4-5-20251001"  # override town-wide default
  roles         = ["reviewer"]                  # opt in to custom roles

  [rig.cost]
  # Overrides [defaults.cost] for this rig.
  daily_budget_usd = 50.0
  warn_at_pct      = 75

  [[rig.formula]]
  name     = "nightly-tests"
  schedule = "0 2 * * *"   # daily at 02:00 UTC

  [[rig.formula]]
  name     = "weekly-refactor"
  schedule = "0 9 * * 1"   # Mondays at 09:00 UTC

[[rig]]
name    = "docs"
repo    = "${PROJECTS_DIR}/docs"
branch  = "main"
enabled = true

  [rig.agents]
  # Minimal rig: no Refinery, fewer Polecats.
  mayor        = true
  witness      = true
  refinery     = false
  deacon       = true
  max_polecats  = 5

  # No [rig.cost] block — inherits [defaults.cost].
  # No [[rig.formula]] — no scheduled workflows.
```
