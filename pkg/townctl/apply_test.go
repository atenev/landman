package townctl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

// writeManifest writes a manifest TOML file into a temp directory and returns
// the full path. Helper for Apply pipeline tests.
func writeManifest(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	return path
}

// minManifest is a valid manifest with no roles (no filesystem checks at apply
// time) and a single rig. Safe to use in dry-run tests.
const minManifest = `
version = "1"

[town]
name = "testtown"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`

// ── Apply — error paths ─────────────────────────────────────────────────────

func TestApply_MissingFile_ReturnsError(t *testing.T) {
	err := townctl.Apply("/nonexistent/path/town.toml", townctl.ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("expected error for missing manifest file, got nil")
	}
}

func TestApply_InvalidTOML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", "this is not valid toml !!!{{{")
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
}

func TestApply_UnsupportedVersion_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", `
version = "99"

[town]
name = "t"
home = "/opt/gt"
`)
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("expected error for unsupported manifest version, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "version") {
		t.Errorf("error should mention version, got: %v", err)
	}
}

func TestApply_MissingSecretVar_ReturnsError(t *testing.T) {
	os.Unsetenv("APPLY_TEST_MISSING_KEY_XYZ")

	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${APPLY_TEST_MISSING_KEY_XYZ}"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("expected error for missing secret env var, got nil")
	}
}

// ── Apply — dry-run success paths ──────────────────────────────────────────

func TestApply_DryRun_NoRoles_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", minManifest)
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply dry-run with valid manifest: %v", err)
	}
}

func TestApply_DryRun_NoRolesNoRigs_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", `
version = "1"

[town]
name = "empty"
home = "/opt/gt"
`)
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply dry-run with empty manifest: %v", err)
	}
}

func TestApply_DryRun_WithIncludes_Succeeds(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "extra.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "extra"
repo   = "/srv/extra"
branch = "main"
`)
	path := writeManifest(t, dir, "town.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

includes = ["extra.toml"]

[[rig]]
name   = "base-rig"
repo   = "/srv/base"
branch = "main"
`)
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply dry-run with includes: %v", err)
	}
}

func TestApply_DryRun_InvalidIncludePattern_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// includes must appear before [town] — keys after a table header belong to
	// that table in TOML, so top-level keys must precede the first section.
	path := writeManifest(t, dir, "town.toml", `
version = "1"
includes = ["[invalid-glob"]

[town]
name = "t"
home = "/opt/gt"
`)
	err := townctl.Apply(path, townctl.ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatal("expected error for invalid include glob pattern, got nil")
	}
}

func TestApply_DryRun_EnvOverlayMissing_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", minManifest)
	// Request env overlay for "staging" but town.staging.toml does not exist.
	err := townctl.Apply(path, townctl.ApplyOptions{
		DryRun: true,
		Env:    "staging",
	})
	if err == nil {
		t.Fatal("expected error for missing env overlay file, got nil")
	}
}

func TestApply_DryRun_EnvOverlayApplied_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", minManifest)
	writeTOML(t, dir, "town.staging.toml", `
version = "1"

[town]
name = "testtown-staging"
home = "/opt/gt"
`)
	err := townctl.Apply(path, townctl.ApplyOptions{
		DryRun: true,
		Env:    "staging",
	})
	if err != nil {
		t.Fatalf("Apply dry-run with valid env overlay: %v", err)
	}
}

// ── Apply — Dolt connection failure ────────────────────────────────────────

func TestApply_DoltConnectionFailure_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, "town.toml", minManifest)
	// Point to a non-existent Dolt server; Connect should fail fast.
	err := townctl.Apply(path, townctl.ApplyOptions{
		DryRun:   false,
		DoltHost: "127.0.0.1",
		DoltPort: 19999, // port unlikely to be in use
		DoltDB:   "gas_town",
		DoltUser: "root",
	})
	if err == nil {
		t.Fatal("expected error for Dolt connection failure, got nil")
	}
}

// ── ResolveIncludes — glob ordering ────────────────────────────────────────

func TestResolveIncludes_GlobOrdering_PatternOrderPreserved(t *testing.T) {
	dir := t.TempDir()
	// Write two files that match different explicit patterns.
	writeTOML(t, dir, "alpha.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "alpha"
repo   = "/srv/alpha"
branch = "main"
`)
	writeTOML(t, dir, "beta.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "beta"
repo   = "/srv/beta"
branch = "main"
`)
	// Request beta first, then alpha — ordering should be preserved.
	included, err := townctl.ResolveIncludes(dir, []string{"beta.toml", "alpha.toml"})
	if err != nil {
		t.Fatalf("ResolveIncludes: %v", err)
	}
	if len(included) != 2 {
		t.Fatalf("expected 2 included manifests, got %d", len(included))
	}
	if included[0].Rigs[0].Name != "beta" {
		t.Errorf("first included rig = %q, want beta (pattern order preserved)", included[0].Rigs[0].Name)
	}
	if included[1].Rigs[0].Name != "alpha" {
		t.Errorf("second included rig = %q, want alpha (pattern order preserved)", included[1].Rigs[0].Name)
	}
}

func TestResolveIncludes_GlobOrdering_DeduplicatesAcrossPatterns(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "shared.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "shared"
repo   = "/srv/shared"
branch = "main"
`)
	// Both patterns match the same file; it should only appear once.
	included, err := townctl.ResolveIncludes(dir, []string{"shared.toml", "*.toml"})
	if err != nil {
		t.Fatalf("ResolveIncludes: %v", err)
	}
	if len(included) != 1 {
		t.Errorf("expected 1 (deduplicated) included manifest, got %d: %+v", len(included), included)
	}
}

// ── MergeIncludes — merge semantics ────────────────────────────────────────

func TestMergeIncludes_DuplicateRoleName_ReturnsError(t *testing.T) {
	base := parseSecretManifest(t, customRoleBase+`
[[role]]
name  = "auditor"
scope = "town"

  [role.identity]
  claude_md = "/opt/gt/roles/auditor/CLAUDE.md"

  [role.trigger]
  type = "manual"

  [role.supervision]
  parent = "mayor"
`)
	// Included manifest also has "auditor" → duplicate error.
	inc := parseSecretManifest(t, customRoleBase+`
[[role]]
name  = "auditor"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/auditor/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"
`)
	if err := townctl.MergeIncludes(base, []*manifest.TownManifest{inc}); err == nil {
		t.Fatal("expected error for duplicate role name, got nil")
	}
}
