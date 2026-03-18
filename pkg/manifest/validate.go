package manifest

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"
	toml "github.com/pelletier/go-toml/v2"
)

var (
	slugRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	cronRE = regexp.MustCompile(`^(\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+)$`)
)

// builtinRoles is the set of Gas Town built-in role names reserved by the gt
// binary. Custom [[role]] definitions must not shadow these names (ADR-0004,
// Decision 4).
var builtinRoles = map[string]struct{}{
	"mayor": {}, "polecat": {}, "witness": {},
	"refinery": {}, "deacon": {}, "dog": {}, "crew": {},
}

// Parse decodes raw TOML bytes into a TownManifest and validates it.
// Validation rules:
//   - version must be "1"
//   - rig names must be unique slugs
//   - witness=true requires mayor=true
//   - max_polecats <= 30
//   - formula schedules must be valid 5-field cron expressions
//   - cost policy: exactly one budget field when block is present; warn_at_pct in [1,99]
//   - role names must not shadow built-in role names
//   - role names must be unique
//   - trigger.type=schedule requires trigger.schedule (valid cron)
//   - trigger.type=event requires trigger.event
//   - supervision.parent must reference a built-in or defined custom role
//   - supervision.reports_to (if set) must reference a built-in or defined custom role
//   - rig.agents.roles entries must reference a defined [[role]] name with scope=rig
//   - town-scoped roles may not be opted into per rig
func Parse(data []byte) (*TownManifest, error) {
	var m TownManifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("toml decode: %w", err)
	}
	if err := validate(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validate(m *TownManifest) error {
	v := validator.New()

	// Register custom tag: slug.
	if err := v.RegisterValidation("slug", func(fl validator.FieldLevel) bool {
		return slugRE.MatchString(fl.Field().String())
	}); err != nil {
		return err
	}

	// Register custom tag: cron (5-field expression).
	if err := v.RegisterValidation("cron", func(fl validator.FieldLevel) bool {
		return cronRE.MatchString(fl.Field().String())
	}); err != nil {
		return err
	}

	if err := v.Struct(m); err != nil {
		return fmt.Errorf("manifest validation: %w", err)
	}

	// Cross-field rules not expressible in struct tags.
	if err := crossValidate(m); err != nil {
		return err
	}
	return nil
}

func crossValidate(m *TownManifest) error {
	// --- Rig uniqueness and interdependency checks ---
	seenRig := make(map[string]struct{}, len(m.Rigs))
	for i, rig := range m.Rigs {
		if _, dup := seenRig[rig.Name]; dup {
			return fmt.Errorf("rig[%d]: duplicate rig name %q", i, rig.Name)
		}
		seenRig[rig.Name] = struct{}{}

		if rig.Agents.Witness && !rig.Agents.Mayor {
			return fmt.Errorf("rig %q: witness=true requires mayor=true", rig.Name)
		}
	}

	// --- Cost policy validation (ADR-0006) ---
	if !m.Defaults.Cost.IsEmpty() {
		if err := validateCostPolicy(m.Defaults.Cost, "[defaults.cost]"); err != nil {
			return err
		}
	}
	for _, rig := range m.Rigs {
		if !rig.Cost.IsEmpty() {
			if err := validateCostPolicy(rig.Cost, fmt.Sprintf("[rig.%s.cost]", rig.Name)); err != nil {
				return err
			}
		}
	}

	// --- Custom role checks (ADR-0004) ---

	// Pass 1: collect role names and check for duplicates and built-in shadowing.
	// Two-pass approach ensures forward references in parent/reports_to are valid.
	roleNames := make(map[string]struct{}, len(m.Roles))
	roleScopeByName := make(map[string]string, len(m.Roles))
	for i, role := range m.Roles {
		// Must not shadow a built-in role (ADR-0004, Decision 4).
		if _, builtin := builtinRoles[role.Name]; builtin {
			return fmt.Errorf("[role.name] %q shadows a built-in Gas Town role", role.Name)
		}
		// Must be unique within the manifest.
		if _, dup := roleNames[role.Name]; dup {
			return fmt.Errorf("role[%d]: duplicate role name %q", i, role.Name)
		}
		roleNames[role.Name] = struct{}{}
		roleScopeByName[role.Name] = role.Scope
	}

	// allKnownRoles includes built-ins so parent/reports_to may reference them.
	allKnownRoles := make(map[string]struct{}, len(roleNames)+len(builtinRoles))
	for name := range builtinRoles {
		allKnownRoles[name] = struct{}{}
	}
	for name := range roleNames {
		allKnownRoles[name] = struct{}{}
	}

	// Pass 2: per-role cross-field checks that require the complete role set.
	for _, role := range m.Roles {
		// Trigger cross-field rules.
		switch role.Trigger.Type {
		case "schedule":
			if role.Trigger.Schedule == "" {
				return fmt.Errorf("role %q: trigger.type=schedule requires trigger.schedule", role.Name)
			}
		case "event":
			if role.Trigger.Event == "" {
				return fmt.Errorf("role %q: trigger.type=event requires trigger.event", role.Name)
			}
		}

		// Supervision parent must be a known role (built-in or custom).
		if _, ok := allKnownRoles[role.Supervision.Parent]; !ok {
			return fmt.Errorf("[role.%s.supervision.parent] unknown role: %q", role.Name, role.Supervision.Parent)
		}
		// reports_to is optional; if set, must also be a known role.
		if role.Supervision.ReportsTo != "" {
			if _, ok := allKnownRoles[role.Supervision.ReportsTo]; !ok {
				return fmt.Errorf("[role.%s.supervision.reports_to] unknown role: %q", role.Name, role.Supervision.ReportsTo)
			}
		}
	}

	// --- Rig role reference checks ---
	for _, rig := range m.Rigs {
		for _, ref := range rig.Agents.Roles {
			if _, defined := roleNames[ref]; !defined {
				return fmt.Errorf("[rig.%s.agents.roles] unknown role: %q", rig.Name, ref)
			}
			// Town-scoped roles are active globally and must not be opted in per rig.
			if roleScopeByName[ref] == "town" {
				return fmt.Errorf("[rig.%s.agents.roles] role %q is town-scoped and cannot be opted in per rig", rig.Name, ref)
			}
		}
	}

	return nil
}

// ValidateApplyTime runs filesystem checks that must succeed before any Dolt
// write. It expands ${VAR} references in claude_md paths using os.ExpandEnv.
// For testing, use ValidateApplyTimeFS with a stub stat function.
func ValidateApplyTime(m *TownManifest) error {
	return ValidateApplyTimeFS(m, func(path string) error {
		_, err := os.Stat(path)
		return err
	})
}

// ValidateApplyTimeFS is the testable variant of ValidateApplyTime.
// The stat argument is called for each resolved claude_md path; return a
// non-nil error to signal that the path does not exist.
func ValidateApplyTimeFS(m *TownManifest, stat func(string) error) error {
	for _, role := range m.Roles {
		path := os.ExpandEnv(role.Identity.ClaudeMD)
		if err := stat(path); err != nil {
			return fmt.Errorf("[role.%s.identity.claude_md] path not found: %s", role.Name, path)
		}
	}
	return nil
}

// validateCostPolicy checks mutual exclusion of budget fields and warn_at_pct range.
func validateCostPolicy(c CostPolicy, ctx string) error {
	var setFields []string
	if c.DailyBudgetUSD != nil {
		setFields = append(setFields, "daily_budget_usd")
	}
	if c.DailyBudgetMessages != nil {
		setFields = append(setFields, "daily_budget_messages")
	}
	if c.DailyBudgetTokens != nil {
		setFields = append(setFields, "daily_budget_tokens")
	}

	switch len(setFields) {
	case 0:
		return fmt.Errorf("%s declares no budget. At least one of daily_budget_usd, daily_budget_messages, daily_budget_tokens is required.", ctx)
	case 1:
		// Valid.
	default:
		return fmt.Errorf("%s sets both %s and %s. Exactly one budget type may be set per cost policy block.",
			ctx, setFields[0], strings.Join(setFields[1:], " and "))
	}
	return nil
}
