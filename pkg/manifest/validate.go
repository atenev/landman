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
	// --- Surveyor lifecycle checks (ADR-0002, dgt-q8q) ---
	// Only validate when non-zero (zero = absent → use built-in default).
	if ct := m.Town.Agents.SurveyorConvergenceThreshold; ct != 0 && (ct <= 0 || ct > 1) {
		return fmt.Errorf("[town.agents.surveyor_convergence_threshold] must be in (0.0, 1.0], got %v", ct)
	}
	if rc := m.Town.Agents.SurveyorRetryCount; rc != 0 && rc < 1 {
		return fmt.Errorf("[town.agents.surveyor_retry_count] must be >= 1, got %d", rc)
	}

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

	// Build extends map for cycle detection: name → extends-target (custom only).
	extendsTarget := make(map[string]string, len(m.Roles))
	for _, role := range m.Roles {
		if role.Identity.Extends != "" {
			extendsTarget[role.Name] = role.Identity.Extends
		}
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

		// extends validation (ADR-0005).
		if role.Identity.Extends != "" {
			// extends must reference a custom role (not a built-in).
			if _, builtin := builtinRoles[role.Identity.Extends]; builtin {
				return fmt.Errorf("[role.%s.identity.extends] %q is a built-in role; extends only supports custom roles",
					role.Name, role.Identity.Extends)
			}
			// extends must reference a defined custom role.
			if _, defined := roleNames[role.Identity.Extends]; !defined {
				return fmt.Errorf("[role.%s.identity.extends] unknown custom role: %q",
					role.Name, role.Identity.Extends)
			}
			// Self-reference is a cycle.
			if role.Identity.Extends == role.Name {
				return fmt.Errorf("[role.%s.identity.extends] role cannot extend itself", role.Name)
			}
			// Walk the extends chain to detect cycles (max depth = len(m.Roles)).
			if err := detectExtendsCycle(role.Name, extendsTarget, len(m.Roles)); err != nil {
				return err
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

// detectExtendsCycle walks the extends chain starting from origin and returns
// an error if a cycle is detected. maxDepth is the maximum number of hops
// before a cycle is assumed (set to len(m.Roles)).
func detectExtendsCycle(origin string, extendsTarget map[string]string, maxDepth int) error {
	visited := make(map[string]struct{}, maxDepth)
	cur := origin
	for i := 0; i <= maxDepth; i++ {
		next, ok := extendsTarget[cur]
		if !ok {
			return nil // chain terminates cleanly
		}
		if next == origin {
			return fmt.Errorf("[role.%s.identity.extends] circular extends chain detected", origin)
		}
		if _, seen := visited[next]; seen {
			return fmt.Errorf("[role.%s.identity.extends] circular extends chain detected", origin)
		}
		visited[next] = struct{}{}
		cur = next
	}
	// Should not reach here with valid input, but guard anyway.
	return fmt.Errorf("[role.%s.identity.extends] extends chain exceeds maximum depth", origin)
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
//
// When a role declares extends, the base role's claude_md path is also checked
// because it is read at apply time to produce the merged output file.
func ValidateApplyTimeFS(m *TownManifest, stat func(string) error) error {
	// Check Surveyor CLAUDE.md when explicitly configured.
	if p := m.Town.Agents.SurveyorClaudeMD; p != "" {
		resolved := os.ExpandEnv(p)
		if err := stat(resolved); err != nil {
			return fmt.Errorf("[town.agents.surveyor_claude_md] path not found: %s", resolved)
		}
	}

	// Build a lookup from role name → resolved claude_md path for extends checks.
	claudeMDByName := make(map[string]string, len(m.Roles))
	for _, role := range m.Roles {
		claudeMDByName[role.Name] = os.ExpandEnv(role.Identity.ClaudeMD)
	}

	for _, role := range m.Roles {
		path := os.ExpandEnv(role.Identity.ClaudeMD)
		if err := stat(path); err != nil {
			return fmt.Errorf("[role.%s.identity.claude_md] path not found: %s", role.Name, path)
		}
		// When extends is set, the base role's claude_md must also exist because
		// apply-time merge reads it to produce the merged file (ADR-0005).
		if role.Identity.Extends != "" {
			basePath, ok := claudeMDByName[role.Identity.Extends]
			if ok && basePath != "" {
				if err := stat(basePath); err != nil {
					return fmt.Errorf("[role.%s.identity.extends] base role %q claude_md path not found: %s",
						role.Name, role.Identity.Extends, basePath)
				}
			}
		}
	}
	return nil
}

// WarnExtensionSlots returns a warning message for each non-empty extension
// placeholder slot encountered in m. Callers (town-ctl apply) should log these
// before proceeding with the apply — the blocks are accepted by the parser but
// ignored at apply time.
//
// Extension slots:
//   - [[rig.role]]: reserved for future per-rig inline role definitions (dgt-bfp).
//     Use global [[role]] entries with scope="rig" via ADR-0004 instead.
func WarnExtensionSlots(m *TownManifest) []string {
	var warnings []string
	for _, rig := range m.Rigs {
		if len(rig.Role) == 0 {
			continue
		}
		name := rig.Name
		for _, slot := range rig.Role {
			detail := ""
			if slot.Name != "" {
				detail = fmt.Sprintf(" (name=%q)", slot.Name)
			}
			warnings = append(warnings, fmt.Sprintf(
				"[rig.%s] [[rig.role]]%s: extension slot not yet implemented — "+
					"ignored at apply time. Use global [[role]] with scope=\"rig\" instead.",
				name, detail,
			))
		}
	}
	return warnings
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
