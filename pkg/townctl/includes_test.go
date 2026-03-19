package townctl_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

const baseManifest = `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "core"
repo   = "/srv/core"
branch = "main"
`

// writeTOML writes content to a temp TOML file and returns the path.
func writeTOML(t *testing.T, dir, filename, content string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ─── ResolveIncludes ──────────────────────────────────────────────────────────

func TestResolveIncludes_MatchesGlob(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "rigs.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "extra"
repo   = "/srv/extra"
branch = "main"
`)
	included, err := townctl.ResolveIncludes(dir, []string{"*.toml"})
	if err != nil {
		t.Fatalf("ResolveIncludes: %v", err)
	}
	if len(included) != 1 {
		t.Fatalf("expected 1 included manifest, got %d", len(included))
	}
	if len(included[0].Rigs) != 1 || included[0].Rigs[0].Name != "extra" {
		t.Errorf("unexpected included rigs: %+v", included[0].Rigs)
	}
}

func TestResolveIncludes_EmptyPatterns(t *testing.T) {
	dir := t.TempDir()
	included, err := townctl.ResolveIncludes(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(included) != 0 {
		t.Errorf("expected 0 includes, got %d", len(included))
	}
}

func TestResolveIncludes_NoMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	included, err := townctl.ResolveIncludes(dir, []string{"nonexistent-*.toml"})
	if err != nil {
		t.Fatalf("no-match glob should not error, got: %v", err)
	}
	if len(included) != 0 {
		t.Errorf("expected 0 includes, got %d", len(included))
	}
}

func TestResolveIncludes_InvalidTOML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bad.toml", "this is not valid toml !!!{{{")
	_, err := townctl.ResolveIncludes(dir, []string{"*.toml"})
	if err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
}

// ─── MergeIncludes ────────────────────────────────────────────────────────────

func TestMergeIncludes_AppendsRigs(t *testing.T) {
	base := parseSecretManifest(t, baseManifest)
	inc := &manifest.TownManifest{
		Rigs: []manifest.RigSpec{{Name: "extra", Repo: "/srv/extra", Branch: "main"}},
	}
	if err := townctl.MergeIncludes(base, []*manifest.TownManifest{inc}); err != nil {
		t.Fatalf("MergeIncludes: %v", err)
	}
	if len(base.Rigs) != 2 {
		t.Errorf("expected 2 rigs after merge, got %d", len(base.Rigs))
	}
}

func TestMergeIncludes_DuplicateRigName_ReturnsError(t *testing.T) {
	base := parseSecretManifest(t, baseManifest) // has rig "core"
	inc := &manifest.TownManifest{
		Rigs: []manifest.RigSpec{{Name: "core", Repo: "/duplicate", Branch: "main"}},
	}
	if err := townctl.MergeIncludes(base, []*manifest.TownManifest{inc}); err == nil {
		t.Fatal("expected error for duplicate rig name, got nil")
	}
}

func TestMergeIncludes_BaseScalarsUnchanged(t *testing.T) {
	base := parseSecretManifest(t, baseManifest)
	origTownName := base.Town.Name
	inc := &manifest.TownManifest{
		// included scalars like Town should be ignored
	}
	if err := townctl.MergeIncludes(base, []*manifest.TownManifest{inc}); err != nil {
		t.Fatalf("MergeIncludes: %v", err)
	}
	if base.Town.Name != origTownName {
		t.Errorf("base town name changed: got %q, want %q", base.Town.Name, origTownName)
	}
}

// ─── ApplyEnvOverlay ──────────────────────────────────────────────────────────

func TestApplyEnvOverlay_FileNotExist_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	base := parseSecretManifest(t, baseManifest)
	err := townctl.ApplyEnvOverlay(base, dir, "staging")
	if err == nil {
		t.Fatal("expected error for missing overlay file, got nil")
	}
}

func TestApplyEnvOverlay_OverridesRig(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "town.staging.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "core"
repo   = "/srv/core-staging"
branch = "staging"
`)
	base := parseSecretManifest(t, baseManifest)
	if err := townctl.ApplyEnvOverlay(base, dir, "staging"); err != nil {
		t.Fatalf("ApplyEnvOverlay: %v", err)
	}
	var coreRig *manifest.RigSpec
	for i, r := range base.Rigs {
		if r.Name == "core" {
			coreRig = &base.Rigs[i]
		}
	}
	if coreRig == nil {
		t.Fatal("core rig not found after overlay")
	}
	if coreRig.Branch != "staging" {
		t.Errorf("core branch = %q, want %q", coreRig.Branch, "staging")
	}
}

func TestApplyEnvOverlay_AppendsNewRig(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "town.dev.toml", `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "dev-rig"
repo   = "/srv/dev"
branch = "dev"
`)
	base := parseSecretManifest(t, baseManifest) // has "core"
	if err := townctl.ApplyEnvOverlay(base, dir, "dev"); err != nil {
		t.Fatalf("ApplyEnvOverlay: %v", err)
	}
	if len(base.Rigs) < 2 {
		t.Errorf("expected at least 2 rigs after overlay appending new rig, got %d", len(base.Rigs))
	}
}
