// Package manifest defines the canonical Go types for the Gas Town town.toml
// declarative topology manifest (ADR-0001).
//
// Struct tags:
//
//	`toml`     — field name in the TOML file
//	`validate` — go-validator constraints applied by town-ctl at apply time
//	`json`     — field name in the derived JSON Schema
//
// Secrets are never written to Dolt. town-ctl resolves env-var references at
// apply time and injects them directly into agent processes (ADR-0001, Decision 4).
package manifest

// TownManifest is the top-level structure parsed from town.toml.
type TownManifest struct {
	// Version is the manifest format version. town-ctl refuses to apply an
	// unknown version (ADR-0001, Decision 5).
	Version  string        `toml:"version"  json:"version"  validate:"required,eq=1"`
	Town     TownConfig    `toml:"town"     json:"town"     validate:"required"`
	Defaults RigDefaults   `toml:"defaults" json:"defaults"`
	Secrets  SecretsConfig `toml:"secrets"  json:"secrets"`
	// Includes lists relative glob patterns for per-rig TOML fragments resolved
	// before the Dolt write (ADR-0001, Decision 6; merge semantics → dgt-cfi).
	Includes []string   `toml:"includes" json:"includes"`
	Rigs     []RigSpec  `toml:"rig"      json:"rig"      validate:"dive"`
	// Roles declares custom agent roles (ADR-0004). Roles are defined globally
	// and opted into per-rig via [rig.agents].roles.
	Roles []RoleSpec `toml:"role" json:"role" validate:"dive"`
}

// TownConfig describes the Gas Town instance itself.
type TownConfig struct {
	Name     string         `toml:"name"      json:"name"      validate:"required,slug"`
	Home     string         `toml:"home"      json:"home"      validate:"required"`
	DoltPort int            `toml:"dolt_port" json:"dolt_port" validate:"omitempty,min=1,max=65535"`
	Agents   TownAgents     `toml:"agents"    json:"agents"`
	// Cost configures town-level operational cost controls (ADR-0006).
	Cost     TownCostConfig `toml:"cost"      json:"cost,omitempty"`
}

// TownCostConfig holds town-level operational parameters for cost enforcement.
// These control Deacon's cost patrol behaviour, not the spend limits themselves
// (which are declared in [rig.cost] and [defaults.cost]).
type TownCostConfig struct {
	// PatrolIntervalSeconds is how often Deacon runs the cost patrol query.
	// Must be >= 10 when set. Defaults to 300 (5 minutes) if 0 or unset.
	PatrolIntervalSeconds int `toml:"patrol_interval_seconds" json:"patrol_interval_seconds,omitempty" validate:"omitempty,min=10"`
}

// TownAgents declares which Gas Town-level agents should run alongside gt.
// These are reconciled by town-ctl as part of apply (ADR-0002, Decision 6).
type TownAgents struct {
	// Surveyor enables the topology reconciler (ADR-0002).
	Surveyor bool `toml:"surveyor" json:"surveyor"`

	// SurveyorModel overrides the Claude model for the Surveyor agent.
	// Defaults to the town-wide [defaults].mayor_model when absent.
	SurveyorModel string `toml:"surveyor_model" json:"surveyor_model,omitempty"`

	// SurveyorClaudeMD is the path to the Surveyor's CLAUDE.md file.
	// Defaults to ${GT_HOME}/roles/surveyor/CLAUDE.md when absent.
	// Path interpolation applies. Must resolve at apply time when set.
	SurveyorClaudeMD string `toml:"surveyor_claude_md" json:"surveyor_claude_md,omitempty"`

	// SurveyorConvergenceThreshold is the fraction of desired topology rows
	// that must match actual state before the Surveyor considers reconciliation
	// converged. Must be in (0.0, 1.0] when set. Defaults to 0.95 when absent
	// (zero value). Cross-field validated in crossValidate.
	SurveyorConvergenceThreshold float64 `toml:"surveyor_convergence_threshold" json:"surveyor_convergence_threshold,omitempty"`

	// SurveyorRetryCount is the number of consecutive failed reconciliation
	// cycles before the Surveyor files a priority-1 Deacon Bead. Must be >= 1
	// when set. Defaults to 3 when absent (zero value). Cross-field validated
	// in crossValidate.
	SurveyorRetryCount int `toml:"surveyor_retry_count" json:"surveyor_retry_count,omitempty"`
}

// RigDefaults supplies values inherited by every rig unless overridden.
type RigDefaults struct {
	MayorModel   string `toml:"mayor_model"   json:"mayor_model"`
	PolekatModel string `toml:"polecat_model" json:"polecat_model"`
	MaxPolecats  int    `toml:"max_polecats"  json:"max_polecats"  validate:"lte=30"`
	// Cost is the default daily budget policy inherited by every rig that has no
	// explicit [rig.cost] block. Cross-field validation in crossValidate.
	Cost CostPolicy `toml:"cost" json:"cost,omitempty"`
}

// SecretsConfig holds references to secrets resolved by town-ctl at apply time.
// Secrets are env-var interpolation expressions (${VAR}) or a path to an
// external secrets file. Values are NEVER written to Dolt (ADR-0001, Decision 4).
type SecretsConfig struct {
	// AnthropicAPIKey is an env-var reference, e.g. "${ANTHROPIC_API_KEY}".
	AnthropicAPIKey string `toml:"anthropic_api_key" json:"anthropic_api_key"`
	// GitHubToken is an env-var reference, e.g. "${GITHUB_TOKEN}".
	GitHubToken string `toml:"github_token" json:"github_token"`
	// File is an optional path to an external secrets TOML file (gitignored).
	// Path interpolation applies (${HOME}, ${GT_HOME}, etc.).
	File string `toml:"file" json:"file,omitempty"`
}

// RigSpec describes a single Gas Town rig (one Git repository + agent pool).
type RigSpec struct {
	Name    string      `toml:"name"    json:"name"    validate:"required,slug"`
	Repo    string      `toml:"repo"    json:"repo"    validate:"required"`
	Branch  string      `toml:"branch"  json:"branch"  validate:"required"`
	Enabled bool        `toml:"enabled" json:"enabled"`
	Agents  AgentConfig `toml:"agents"  json:"agents"`
	// Formulas lists scheduled Formula definitions for this rig.
	Formulas []FormulaRef `toml:"formula" json:"formula" validate:"dive"`
	// Cost is the per-rig daily budget policy (overrides [defaults.cost]).
	// Cross-field validation in crossValidate.
	Cost CostPolicy `toml:"cost" json:"cost,omitempty"`
	// Role is a reserved extension slot for future per-rig inline role
	// definitions (dgt-bfp). Accepted by the parser; town-ctl emits a warning
	// and ignores entries at apply time. Use global [[role]] entries with
	// scope="rig" for the current custom-role mechanism (ADR-0004).
	Role []RigRoleSlot `toml:"role" json:"role,omitempty"`
}

// RigRoleSlot is a reserved extension placeholder for future per-rig role
// definitions. It accepts any TOML keys without error so that users who
// speculatively add [[rig.role]] blocks do not get parse failures. town-ctl
// logs a warning and does not process these entries (ADR-0004 extension point,
// tracked by dgt-bfp).
type RigRoleSlot struct {
	// Name holds the optional name field for diagnostic messages. All other
	// keys in the block are silently accepted by the TOML decoder.
	Name string `toml:"name" json:"name,omitempty"`
}

// AgentConfig specifies which agent roles are active on a rig and their
// per-rig overrides. A field value of false disables that role; true means
// enabled with all other settings inherited from [defaults].
type AgentConfig struct {
	Mayor    bool `toml:"mayor"    json:"mayor"`
	Witness  bool `toml:"witness"  json:"witness"`
	Refinery bool `toml:"refinery" json:"refinery"`
	Deacon   bool `toml:"deacon"   json:"deacon"`

	// MaxPolecats overrides [defaults].max_polecats for this rig.
	MaxPolecats int `toml:"max_polecats" json:"max_polecats,omitempty" validate:"omitempty,min=1,lte=30"`

	// PolekatModel overrides [defaults].polecat_model for this rig.
	PolekatModel string `toml:"polecat_model" json:"polecat_model,omitempty"`

	// MayorClaudeMD is the path to the Mayor's CLAUDE.md for this rig.
	// Path interpolation applies (${GT_HOME}, etc.).
	MayorClaudeMD string `toml:"mayor_claude_md" json:"mayor_claude_md,omitempty"`

	// Roles lists the names of custom [[role]] entries to activate on this rig.
	// Each name must match a globally defined [[role]] with scope="rig".
	// town-scoped roles need no entry here — they are active globally.
	Roles []string `toml:"roles" json:"roles,omitempty"`
}

// FormulaRef references a scheduled Formula workflow declared under [[rig.formula]].
type FormulaRef struct {
	Name     string `toml:"name"     json:"name"     validate:"required"`
	Schedule string `toml:"schedule" json:"schedule" validate:"required,cron"`
}

// RoleSpec declares a custom agent role (ADR-0004). Roles are defined globally
// in town.toml and stored in desired_custom_roles. Per-rig activation is via
// desired_rig_custom_roles (scope=rig) or implicit (scope=town).
type RoleSpec struct {
	// Name is the unique slug identifier for this role.
	// Must not shadow a built-in role name.
	Name        string          `toml:"name"        json:"name"        validate:"required,slug"`
	Description string          `toml:"description" json:"description,omitempty"`
	// Scope controls whether this role is rig-scoped (per-rig opt-in required)
	// or town-scoped (active on every rig automatically).
	Scope       string          `toml:"scope"       json:"scope"       validate:"required,oneof=rig town"`
	Lifespan    string          `toml:"lifespan"    json:"lifespan"    validate:"omitempty,oneof=ephemeral persistent"`
	Identity    RoleIdentity    `toml:"identity"    json:"identity"    validate:"required"`
	Trigger     RoleTrigger     `toml:"trigger"     json:"trigger"     validate:"required"`
	Supervision RoleSupervision `toml:"supervision" json:"supervision" validate:"required"`
	Resources   RoleResources   `toml:"resources"   json:"resources"`
}

// RoleIdentity specifies how the role presents itself as a Claude Code agent.
type RoleIdentity struct {
	// ClaudeMD is the path to this role's CLAUDE.md file.
	// Path interpolation applies. Must resolve at apply time (ADR-0004, Decision 2).
	// When Extends is set, ClaudeMD provides the override layer that is appended
	// after the base role's CLAUDE.md content (ADR-0005).
	ClaudeMD string `toml:"claude_md" json:"claude_md" validate:"required"`
	// Extends is the name of another custom [[role]] whose CLAUDE.md content is
	// prepended before this role's ClaudeMD content (ADR-0005). The merge is
	// performed at apply time; the merged file is written to
	// ${GT_HOME}/roles/merged/<name>.md and stored as claude_md_path in Dolt.
	// Optional. Only custom role names are valid — built-in roles are excluded
	// because their CLAUDE.md paths are not declared in the manifest.
	Extends string `toml:"extends" json:"extends,omitempty"`
	// Model overrides the Claude model for this role. Inherits from rig defaults if empty.
	Model string `toml:"model" json:"model,omitempty"`
}

// RoleTrigger defines when a custom role agent is spawned.
type RoleTrigger struct {
	// Type determines the activation mechanism.
	// bead_assigned: wakes when a Bead with assignee=<role-name> appears.
	// schedule:      cron-driven; requires Schedule to be set.
	// event:         CLAUDE.md polls for event Beads; requires Event to be set.
	// manual:        human triggers via Mayor Bead.
	Type     string `toml:"type"     json:"type"               validate:"required,oneof=bead_assigned schedule event manual"`
	Schedule string `toml:"schedule" json:"schedule,omitempty" validate:"omitempty,cron"`
	Event    string `toml:"event"    json:"event,omitempty"`
}

// RoleSupervision declares where this role sits in the Gas Town agent hierarchy.
type RoleSupervision struct {
	// Parent is the built-in or custom role that supervises this one.
	// Required — every role must have a supervisor (ADR-0004, Decision 3).
	Parent    string `toml:"parent"     json:"parent"               validate:"required"`
	// ReportsTo is the escalation target. Defaults to Parent if empty.
	ReportsTo string `toml:"reports_to" json:"reports_to,omitempty"`
}

// RoleResources constrains the number of instances that can run simultaneously.
type RoleResources struct {
	// MaxInstances sets the capacity ceiling. Default 1.
	MaxInstances int `toml:"max_instances" json:"max_instances,omitempty" validate:"omitempty,min=1"`
}

// CostPolicy defines a daily spend limit for a rig or as a global default (ADR-0006).
//
// Exactly one of DailyBudgetUSD, DailyBudgetMessages, or DailyBudgetTokens must be
// set when the cost block is present. If all three are nil the block is considered
// absent. Cross-field mutual-exclusion validation is in crossValidate.
//
// Pointer fields distinguish "not set" from "set to zero".
type CostPolicy struct {
	// DailyBudgetUSD is the maximum daily cost in US dollars. Must be > 0.
	DailyBudgetUSD *float64 `toml:"daily_budget_usd"      json:"daily_budget_usd,omitempty"      validate:"omitempty,gt=0"`
	// DailyBudgetMessages is the maximum daily Claude API message count. Must be >= 1.
	DailyBudgetMessages *int64 `toml:"daily_budget_messages" json:"daily_budget_messages,omitempty" validate:"omitempty,min=1"`
	// DailyBudgetTokens is the maximum daily token count (input + output). Must be >= 1.
	DailyBudgetTokens *int64 `toml:"daily_budget_tokens"   json:"daily_budget_tokens,omitempty"   validate:"omitempty,min=1"`
	// WarnAtPct triggers a warning when budget usage reaches this percentage (1–99).
	// Defaults to 80 at apply time when the cost block is present and warn_at_pct is unset.
	WarnAtPct *int `toml:"warn_at_pct" json:"warn_at_pct,omitempty" validate:"omitempty,min=1,max=99"`
}

// IsEmpty reports whether the CostPolicy has no fields set (block was absent in TOML).
func (c CostPolicy) IsEmpty() bool {
	return c.DailyBudgetUSD == nil &&
		c.DailyBudgetMessages == nil &&
		c.DailyBudgetTokens == nil &&
		c.WarnAtPct == nil
}
