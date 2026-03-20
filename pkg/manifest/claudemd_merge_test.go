package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
)

func TestMergeClaudeMDFiles_Concatenates(t *testing.T) {
	dir := t.TempDir()
	baseFile := filepath.Join(dir, "base.md")
	overrideFile := filepath.Join(dir, "override.md")
	if err := os.WriteFile(baseFile, []byte("# Base\nBase content."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overrideFile, []byte("# Override\nOverride content."), 0o600); err != nil {
		t.Fatal(err)
	}

	merged, err := manifest.MergeClaudeMDFiles(baseFile, overrideFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(merged, "Base content.") {
		t.Error("merged output missing base content")
	}
	if !strings.Contains(merged, "Override content.") {
		t.Error("merged output missing override content")
	}
	// Base must appear before override.
	baseIdx := strings.Index(merged, "Base content.")
	overrideIdx := strings.Index(merged, "Override content.")
	if baseIdx >= overrideIdx {
		t.Errorf("expected base content before override content, got base@%d override@%d",
			baseIdx, overrideIdx)
	}
}

func TestMergeClaudeMDFiles_MissingBase(t *testing.T) {
	dir := t.TempDir()
	overrideFile := filepath.Join(dir, "override.md")
	if err := os.WriteFile(overrideFile, []byte("override"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.MergeClaudeMDFiles("/nonexistent/base.md", overrideFile); err == nil {
		t.Fatal("expected error for missing base file, got nil")
	}
}

func TestMergeClaudeMDFiles_MissingOverride(t *testing.T) {
	dir := t.TempDir()
	baseFile := filepath.Join(dir, "base.md")
	if err := os.WriteFile(baseFile, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.MergeClaudeMDFiles(baseFile, "/nonexistent/override.md"); err == nil {
		t.Fatal("expected error for missing override file, got nil")
	}
}

func TestResolveExtendsChain_NoExtends(t *testing.T) {
	roles := []manifest.RoleSpec{
		makeRole("base", ""),
	}
	chain, err := manifest.ResolveExtendsChain("base", roles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 1 || chain[0] != "/opt/base/CLAUDE.md" {
		t.Errorf("chain = %v, want [/opt/base/CLAUDE.md]", chain)
	}
}

func TestResolveExtendsChain_SingleHop(t *testing.T) {
	roles := []manifest.RoleSpec{
		makeRole("base", ""),
		makeRole("derived", "base"),
	}
	chain, err := manifest.ResolveExtendsChain("derived", roles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"/opt/base/CLAUDE.md", "/opt/derived/CLAUDE.md"}
	if len(chain) != 2 || chain[0] != want[0] || chain[1] != want[1] {
		t.Errorf("chain = %v, want %v", chain, want)
	}
}

func TestResolveExtendsChain_ThreeHops(t *testing.T) {
	roles := []manifest.RoleSpec{
		makeRole("root", ""),
		makeRole("mid", "root"),
		makeRole("leaf", "mid"),
	}
	chain, err := manifest.ResolveExtendsChain("leaf", roles)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 3 {
		t.Errorf("expected 3-element chain, got %v", chain)
	}
}

func TestMergeExtendsChain_SingleFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.md")
	if err := os.WriteFile(f, []byte("only content"), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err := manifest.MergeExtendsChain([]string{f})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged != "only content" {
		t.Errorf("merged = %q, want %q", merged, "only content")
	}
}

func TestMergeExtendsChain_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		filepath.Join(dir, "a.md"),
		filepath.Join(dir, "b.md"),
		filepath.Join(dir, "c.md"),
	}
	for i, f := range files {
		content := string(rune('A'+i)) + " content"
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	merged, err := manifest.MergeExtendsChain(files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"A content", "B content", "C content"} {
		if !strings.Contains(merged, want) {
			t.Errorf("merged missing %q; got: %q", want, merged)
		}
	}
	// Order: A before B before C.
	if strings.Index(merged, "A content") > strings.Index(merged, "B content") {
		t.Error("A content should appear before B content")
	}
	if strings.Index(merged, "B content") > strings.Index(merged, "C content") {
		t.Error("B content should appear before C content")
	}
}

func TestMergeExtendsChain_Empty(t *testing.T) {
	if _, err := manifest.MergeExtendsChain(nil); err == nil {
		t.Fatal("expected error for empty chain, got nil")
	}
}

// makeRole creates a minimal RoleSpec for testing the extends chain resolver.
func makeRole(name, extends string) manifest.RoleSpec {
	return manifest.RoleSpec{
		Name:  name,
		Scope: "rig",
		Identity: manifest.RoleIdentity{
			ClaudeMD: "/opt/" + name + "/CLAUDE.md",
			Extends:  extends,
		},
		Trigger: manifest.RoleTrigger{
			Type: "bead_assigned",
		},
		Supervision: manifest.RoleSupervision{
			Parent: "witness",
		},
	}
}
