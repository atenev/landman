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
	Name     string      `toml:"name"      json:"name"      validate:"required,slug"`
	Home     string      `toml:"home"      json:"home"      validate:"required"`
	DoltPort int         `toml:"dolt_port" json:"dolt_port" validate:"omitempty,min=1,max=65535"`
	Agents   TownAgents  `toml:"agents"    json:"agents"`
}

// TownAgents declares which Gas Town-level agents should run alongside gt.
// These are reconciled by town-ctl as part of apply (ADR-0002, Decision 6).
type TownAgents struct {
	// Surveyor enables the topology reconciler (ADR-0002).
	Surveyor bool `toml:"surveyor" json:"surveyor"`
}

// RigDefaults supplies values inherited by every rig unless overridden.
type RigDefaults struct {
	MayorModel   string `toml:"mayor_model"   json:"mayor_model"`
	PolekatModel string `toml:"polecat_model" json:"polecat_model"`
	MaxPolecats  int    `toml:"max_polecats"  json:"max_polecats"  validate:"lte=30"`
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
	ClaudeMD string `toml:"claude_md" json:"claude_md" validate:"required"`
	// Model overrides the Claude model for this role. Inherits from rig defaults if empty.
	Model    string `toml:"model"     json:"model,omitempty"`
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
