package manifest

import (
	"fmt"
	"regexp"

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
//   - role names must not shadow built-in role names
//   - role names must be unique
//   - trigger.type=schedule requires trigger.schedule
//   - trigger.type=event requires trigger.event
//   - rig.agents.roles entries must reference a defined [[role]] name
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

	// --- Custom role checks (ADR-0004) ---
	seenRole := make(map[string]struct{}, len(m.Roles))
	for i, role := range m.Roles {
		// Must not shadow a built-in role.
		if _, builtin := builtinRoles[role.Name]; builtin {
			return fmt.Errorf("role[%d]: name %q shadows a built-in role — choose a different name", i, role.Name)
		}
		// Must be unique within the manifest.
		if _, dup := seenRole[role.Name]; dup {
			return fmt.Errorf("role[%d]: duplicate role name %q", i, role.Name)
		}
		seenRole[role.Name] = struct{}{}

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
	}

	// --- Rig role reference checks ---
	for _, rig := range m.Rigs {
		for _, ref := range rig.Agents.Roles {
			if _, defined := seenRole[ref]; !defined {
				return fmt.Errorf("rig %q: agents.roles references undefined role %q", rig.Name, ref)
			}
		}
	}

	return nil
}
