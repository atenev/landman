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

// Parse decodes raw TOML bytes into a TownManifest and validates it.
// Validation rules:
//   - version must be "1"
//   - rig names must be unique slugs
//   - witness=true requires mayor=true
//   - max_polecats <= 30
//   - formula schedules must be valid 5-field cron expressions
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
	seen := make(map[string]struct{}, len(m.Rigs))
	for i, rig := range m.Rigs {
		if _, dup := seen[rig.Name]; dup {
			return fmt.Errorf("rig[%d]: duplicate rig name %q", i, rig.Name)
		}
		seen[rig.Name] = struct{}{}

		if rig.Agents.Witness && !rig.Agents.Mayor {
			return fmt.Errorf("rig %q: witness=true requires mayor=true", rig.Name)
		}
	}
	return nil
}
