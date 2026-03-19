// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements include resolution and env overlay merge (ADR-0001,
// Decision 6; ADR-0008).
package townctl

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tenev/dgt/pkg/manifest"
)

// ResolveIncludes loads each TOML file matching any pattern in patterns
// (relative to dir), parses it, and returns the slice of parsed manifests.
// Patterns are standard filepath globs. Returns an error if any glob fails or
// any matched file cannot be parsed.
func ResolveIncludes(dir string, patterns []string) ([]*manifest.TownManifest, error) {
	var included []*manifest.TownManifest
	seen := map[string]struct{}{}
	for _, pat := range patterns {
		if !filepath.IsAbs(pat) {
			pat = filepath.Join(dir, pat)
		}
		matches, err := filepath.Glob(pat)
		if err != nil {
			return nil, fmt.Errorf("includes: %s: %w", pat, err)
		}
		for _, path := range matches {
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, fmt.Errorf("includes: %s: %w", path, err)
			}
			if _, dup := seen[abs]; dup {
				continue // same file matched by multiple patterns
			}
			seen[abs] = struct{}{}
			data, err := os.ReadFile(abs)
			if err != nil {
				return nil, fmt.Errorf("includes: %s: %w", abs, err)
			}
			m, err := manifest.Parse(data)
			if err != nil {
				return nil, fmt.Errorf("includes: %s: %w", abs, err)
			}
			included = append(included, m)
		}
	}
	return included, nil
}

// MergeIncludes merges included manifests into base in-place.
//
// Merge rules (ADR-0001 Decision 6, ADR-0008):
//   - [[rig]] entries: append included rigs to base rig list.
//   - [[role]] entries: append included roles to base role list.
//   - Scalar top-level fields ([town], [defaults], [secrets]): base wins; included
//     scalars are ignored.
//   - Duplicate rig names across base + includes: hard error.
//   - Duplicate role names across base + includes: hard error.
func MergeIncludes(base *manifest.TownManifest, included []*manifest.TownManifest) error {
	rigNames := make(map[string]struct{}, len(base.Rigs))
	for _, r := range base.Rigs {
		rigNames[r.Name] = struct{}{}
	}

	roleNames := make(map[string]struct{}, len(base.Roles))
	for _, r := range base.Roles {
		roleNames[r.Name] = struct{}{}
	}

	for _, inc := range included {
		for _, rig := range inc.Rigs {
			if _, dup := rigNames[rig.Name]; dup {
				return fmt.Errorf("duplicate rig name %q in included file", rig.Name)
			}
			rigNames[rig.Name] = struct{}{}
			base.Rigs = append(base.Rigs, rig)
		}
		for _, role := range inc.Roles {
			if _, dup := roleNames[role.Name]; dup {
				return fmt.Errorf("duplicate role name %q in included file", role.Name)
			}
			roleNames[role.Name] = struct{}{}
			base.Roles = append(base.Roles, role)
		}
	}
	return nil
}

// ApplyEnvOverlay loads the overlay file <dir>/town.<envName>.toml and applies
// it over base. Overlay rules (ADR-0008):
//   - Scalar fields: overlay value wins.
//   - [[rig]] entries: overlay rig overrides base rig with same name; new names
//     are appended.
//   - [[role]] entries: same as rigs.
//
// Returns an error if the overlay file does not exist.
func ApplyEnvOverlay(base *manifest.TownManifest, dir, envName string) error {
	path := filepath.Join(dir, fmt.Sprintf("town.%s.toml", envName))
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("env overlay %s: %w", path, err)
	}
	overlay, err := manifest.Parse(data)
	if err != nil {
		return fmt.Errorf("env overlay %s: %w", path, err)
	}

	// Scalar fields: overlay wins.
	if overlay.Town.Name != "" {
		base.Town = overlay.Town
	}
	if overlay.Defaults.MayorModel != "" || overlay.Defaults.MaxPolecats != 0 {
		base.Defaults = overlay.Defaults
	}
	if overlay.Secrets.AnthropicAPIKey != "" || overlay.Secrets.File != "" {
		base.Secrets = overlay.Secrets
	}

	// Rigs: overlay rig overrides rig with same name; new rigs are appended.
	overlayRigByName := make(map[string]int, len(overlay.Rigs))
	for i, r := range overlay.Rigs {
		overlayRigByName[r.Name] = i
	}
	for i, rig := range base.Rigs {
		if idx, ok := overlayRigByName[rig.Name]; ok {
			base.Rigs[i] = overlay.Rigs[idx]
			delete(overlayRigByName, rig.Name)
		}
	}
	for name, idx := range overlayRigByName {
		_ = name
		base.Rigs = append(base.Rigs, overlay.Rigs[idx])
	}

	// Roles: same pattern as rigs.
	overlayRoleByName := make(map[string]int, len(overlay.Roles))
	for i, r := range overlay.Roles {
		overlayRoleByName[r.Name] = i
	}
	for i, role := range base.Roles {
		if idx, ok := overlayRoleByName[role.Name]; ok {
			base.Roles[i] = overlay.Roles[idx]
			delete(overlayRoleByName, role.Name)
		}
	}
	for name, idx := range overlayRoleByName {
		_ = name
		base.Roles = append(base.Roles, overlay.Roles[idx])
	}

	return nil
}
