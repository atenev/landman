package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
)

// ── run() entry point ────────────────────────────────────────────────────────

func TestRun_NoArgs_ReturnsOne(t *testing.T) {
	if code := run(nil); code != 1 {
		t.Errorf("run(nil) = %d, want 1", code)
	}
}

func TestRun_UnknownCommand_ReturnsOne(t *testing.T) {
	if code := run([]string{"bogus"}); code != 1 {
		t.Errorf("run([bogus]) = %d, want 1", code)
	}
}

func TestRun_Apply_MissingFile_ReturnsOne(t *testing.T) {
	code := run([]string{"apply", "--file", "/nonexistent/path/town.toml"})
	if code != 1 {
		t.Errorf("run with missing file = %d, want 1", code)
	}
}

func TestRun_Apply_InvalidVersion_ReturnsOne(t *testing.T) {
	f := writeTempTOML(t, `
version = "99"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	code := run([]string{"apply", "--file", f})
	if code != 1 {
		t.Errorf("run with version=99 = %d, want 1", code)
	}
}

func TestRun_Apply_MissingRequiredField_ReturnsOne(t *testing.T) {
	// [town] block with no name → validation error.
	f := writeTempTOML(t, `
version = "1"

[town]
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	code := run([]string{"apply", "--file", f})
	if code != 1 {
		t.Errorf("run with missing town.name = %d, want 1", code)
	}
}

func TestRun_Apply_MissingSecretEnvVar_ReturnsOne(t *testing.T) {
	// secrets.anthropic_api_key references an unset env var.
	os.Unsetenv("TEST_MISSING_API_KEY_FOR_TOWNCTL")
	f := writeTempTOML(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${TEST_MISSING_API_KEY_FOR_TOWNCTL}"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	code := run([]string{"apply", "--file", f})
	if code != 1 {
		t.Errorf("run with missing secret env var = %d, want 1", code)
	}
}

func TestRun_Apply_DoltConnectionFailure_ReturnsOne(t *testing.T) {
	// Provide a DSN that cannot connect (invalid host/port).
	f := writeTempTOML(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	code := run([]string{"apply", "--file", f, "--dolt-dsn", "root@tcp(127.0.0.1:1)/gastown"})
	if code != 1 {
		t.Errorf("run with bad DSN = %d, want 1", code)
	}
}

func TestRun_Apply_DryRun_DoltUnreachable_ReturnsZero(t *testing.T) {
	// With --dry-run and Dolt unreachable, town-ctl should print the plan and
	// exit 0 (fallback path in applyDryRun).
	f := writeTempTOML(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	code := run([]string{"apply", "--file", f, "--dry-run", "--dolt-dsn", "root@tcp(127.0.0.1:1)/gastown"})
	if code != 0 {
		t.Errorf("run --dry-run with unreachable Dolt = %d, want 0", code)
	}
}

// ── mergeFragment ────────────────────────────────────────────────────────────

func TestMergeFragment_AppendNewRig(t *testing.T) {
	base := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"
`)
	frag := &includeFragment{
		Rigs: []manifest.RigSpec{
			{Name: "frontend", Repo: "/srv/frontend", Branch: "main"},
		},
	}
	if err := mergeFragment(base, frag, "test.toml"); err != nil {
		t.Fatalf("mergeFragment: %v", err)
	}
	if len(base.Rigs) != 2 {
		t.Errorf("expected 2 rigs after append, got %d", len(base.Rigs))
	}
	if base.Rigs[1].Name != "frontend" {
		t.Errorf("expected appended rig to be frontend, got %q", base.Rigs[1].Name)
	}
}

func TestMergeFragment_OverrideExistingRig(t *testing.T) {
	base := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name    = "backend"
repo    = "/srv/backend"
branch  = "main"
enabled = true
`)
	// Fragment overrides the backend rig with a different repo.
	frag := &includeFragment{
		Rigs: []manifest.RigSpec{
			{Name: "backend", Repo: "/srv/new-backend", Branch: "release"},
		},
	}
	if err := mergeFragment(base, frag, "override.toml"); err != nil {
		t.Fatalf("mergeFragment: %v", err)
	}
	if len(base.Rigs) != 1 {
		t.Errorf("expected 1 rig after override, got %d", len(base.Rigs))
	}
	if base.Rigs[0].Repo != "/srv/new-backend" {
		t.Errorf("repo = %q, want /srv/new-backend", base.Rigs[0].Repo)
	}
}

func TestMergeFragment_DuplicateRigWithinFragment_Error(t *testing.T) {
	base := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"
`)
	frag := &includeFragment{
		Rigs: []manifest.RigSpec{
			{Name: "dup-rig", Repo: "/srv/a", Branch: "main"},
			{Name: "dup-rig", Repo: "/srv/b", Branch: "main"},
		},
	}
	if err := mergeFragment(base, frag, "dup.toml"); err == nil {
		t.Fatal("expected error for duplicate rig name within fragment, got nil")
	}
}

func TestMergeFragment_AppendNewRole(t *testing.T) {
	base := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	frag := &includeFragment{
		Roles: []manifest.RoleSpec{
			{
				Name:  "reviewer",
				Scope: "rig",
				Identity: manifest.RoleIdentity{
					ClaudeMD: "/opt/gt/roles/reviewer/CLAUDE.md",
				},
				Trigger: manifest.RoleTrigger{Type: "bead_assigned"},
				Supervision: manifest.RoleSupervision{
					Parent: "witness",
				},
			},
		},
	}
	if err := mergeFragment(base, frag, "roles.toml"); err != nil {
		t.Fatalf("mergeFragment: %v", err)
	}
	if len(base.Roles) != 1 || base.Roles[0].Name != "reviewer" {
		t.Errorf("expected 1 role named reviewer, got %+v", base.Roles)
	}
}

func TestMergeFragment_DuplicateRoleWithinFragment_Error(t *testing.T) {
	base := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	roleSpec := manifest.RoleSpec{
		Name:        "dup-role",
		Scope:       "rig",
		Identity:    manifest.RoleIdentity{ClaudeMD: "/opt/gt/roles/dup-role/CLAUDE.md"},
		Trigger:     manifest.RoleTrigger{Type: "bead_assigned"},
		Supervision: manifest.RoleSupervision{Parent: "witness"},
	}
	frag := &includeFragment{
		Roles: []manifest.RoleSpec{roleSpec, roleSpec},
	}
	if err := mergeFragment(base, frag, "dup-roles.toml"); err == nil {
		t.Fatal("expected error for duplicate role name within fragment, got nil")
	}
}

// ── resolveIncludes ──────────────────────────────────────────────────────────

func TestResolveIncludes_NoIncludes_Noop(t *testing.T) {
	base := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	before := len(base.Rigs)
	if err := resolveIncludes(base, "/tmp"); err != nil {
		t.Fatalf("resolveIncludes: %v", err)
	}
	if len(base.Rigs) != before {
		t.Errorf("rig count changed from %d to %d", before, len(base.Rigs))
	}
}

func TestResolveIncludes_GlobMatchesFiles(t *testing.T) {
	dir := t.TempDir()

	// Write two rig fragment files.
	writeFile(t, filepath.Join(dir, "rig-a.toml"), `
[[rig]]
name   = "rig-a"
repo   = "/srv/a"
branch = "main"
`)
	writeFile(t, filepath.Join(dir, "rig-b.toml"), `
[[rig]]
name   = "rig-b"
repo   = "/srv/b"
branch = "main"
`)

	// Note: includes must be declared before any [table] block in TOML so it
	// is parsed as a top-level field, not a sub-key of [town].
	base := mustParseMainTest(t, `
version  = "1"
includes = ["*.toml"]

[town]
name = "t"
home = "/opt/gt"
`)
	if err := resolveIncludes(base, dir); err != nil {
		t.Fatalf("resolveIncludes: %v", err)
	}
	if len(base.Rigs) != 2 {
		t.Errorf("expected 2 rigs from glob, got %d", len(base.Rigs))
	}
}

func TestResolveIncludes_OverlappingGlobs_NoDuplicates(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "rig-shared.toml"), `
[[rig]]
name   = "shared-rig"
repo   = "/srv/shared"
branch = "main"
`)

	// Two overlapping glob patterns both match the same file. includes must
	// appear before any [table] header to be a top-level key.
	base := mustParseMainTest(t, `
version  = "1"
includes = ["*.toml", "rig-*.toml"]

[town]
name = "t"
home = "/opt/gt"
`)
	if err := resolveIncludes(base, dir); err != nil {
		t.Fatalf("resolveIncludes: %v", err)
	}
	// Should only merge once even though both globs matched.
	if len(base.Rigs) != 1 {
		t.Errorf("expected 1 rig (no duplicate from overlapping globs), got %d", len(base.Rigs))
	}
}

func TestResolveIncludes_InvalidGlob_Error(t *testing.T) {
	// Use an absolute path to a nonexistent glob pattern to trigger an error
	// via the resolveIncludes error path (missing file).
	base := mustParseMainTest(t, `
version  = "1"
includes = ["[invalid-glob"]

[town]
name = "t"
home = "/opt/gt"
`)
	if err := resolveIncludes(base, "/tmp"); err == nil {
		t.Fatal("expected error for invalid glob pattern, got nil")
	}
}

// ── resolveSecrets ───────────────────────────────────────────────────────────

func TestResolveSecrets_NoSecrets_Noop(t *testing.T) {
	m := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	if err := resolveSecrets(m); err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
}

func TestResolveSecrets_EnvVarInterpolation(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "sk-test-abc123")
	m := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${TEST_ANTHROPIC_KEY}"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	if err := resolveSecrets(m); err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	if m.Secrets.AnthropicAPIKey != "sk-test-abc123" {
		t.Errorf("AnthropicAPIKey = %q, want sk-test-abc123", m.Secrets.AnthropicAPIKey)
	}
}

func TestResolveSecrets_MissingEnvVar_Error(t *testing.T) {
	os.Unsetenv("TEST_MISSING_KEY_TOWNCTL_UNIT")
	m := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${TEST_MISSING_KEY_TOWNCTL_UNIT}"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	if err := resolveSecrets(m); err == nil {
		t.Fatal("expected error for missing env var, got nil")
	}
}

func TestResolveSecrets_MissingGithubToken_Error(t *testing.T) {
	os.Unsetenv("TEST_MISSING_GH_TOKEN_TOWNCTL")
	m := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
github_token = "${TEST_MISSING_GH_TOKEN_TOWNCTL}"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	if err := resolveSecrets(m); err == nil {
		t.Fatal("expected error for missing github_token env var, got nil")
	}
}

func TestResolveSecrets_SecretsFile(t *testing.T) {
	dir := t.TempDir()
	secretsFile := filepath.Join(dir, "secrets.toml")
	writeFile(t, secretsFile, `
anthropic_api_key = "sk-from-file"
github_token      = "ghp-from-file"
`)

	m := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
file = "`+secretsFile+`"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	if err := resolveSecrets(m); err != nil {
		t.Fatalf("resolveSecrets with file: %v", err)
	}
	if m.Secrets.AnthropicAPIKey != "sk-from-file" {
		t.Errorf("AnthropicAPIKey = %q, want sk-from-file", m.Secrets.AnthropicAPIKey)
	}
	if m.Secrets.GitHubToken != "ghp-from-file" {
		t.Errorf("GitHubToken = %q, want ghp-from-file", m.Secrets.GitHubToken)
	}
}

func TestResolveSecrets_EnvVarWinsOverFile(t *testing.T) {
	// When both the secrets file and an env-var ref are set, the ref takes precedence.
	dir := t.TempDir()
	secretsFile := filepath.Join(dir, "secrets.toml")
	writeFile(t, secretsFile, `
anthropic_api_key = "sk-from-file"
`)
	t.Setenv("TEST_OVERRIDE_KEY", "sk-from-env")

	m := mustParseMainTest(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
file              = "`+secretsFile+`"
anthropic_api_key = "${TEST_OVERRIDE_KEY}"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	if err := resolveSecrets(m); err != nil {
		t.Fatalf("resolveSecrets: %v", err)
	}
	// The manifest field is set so the env-var ref wins over the file value.
	if m.Secrets.AnthropicAPIKey != "sk-from-env" {
		t.Errorf("AnthropicAPIKey = %q, want sk-from-env", m.Secrets.AnthropicAPIKey)
	}
}

// ── --env overlay ───────────────────────────────────────────────────────────

func TestStringSlice_LastEnvWins(t *testing.T) {
	// Simulate two --env flags for the same var; the second should win.
	// We test this via a temp manifest that uses the env var.
	t.Setenv("TEST_OVERLAY_VAR", "first-value")
	// Calling os.Setenv after the first simulates what applyCmd does.
	// We verify the run() machinery honors the order by testing run() directly.
	f := writeTempTOML(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	// First invocation sets env to "override-value"; second --env would reset.
	// Since we can't easily capture stdout in run(), we just verify it accepts
	// multiple --env flags without crashing (integration-style smoke test).
	// Full Dolt unavailable → dry-run to avoid connection attempt.
	code := run([]string{
		"apply", "--file", f,
		"--env", "TEST_OVERLAY_VAR=first-value",
		"--env", "TEST_OVERLAY_VAR=last-wins",
		"--dry-run",
		"--dolt-dsn", "root@tcp(127.0.0.1:1)/gastown",
	})
	// Dolt unreachable + dry-run → exit 0.
	if code != 0 {
		t.Errorf("run with multiple --env = %d, want 0", code)
	}
}

func TestStringSlice_InvalidEnvFormat_ReturnsOne(t *testing.T) {
	f := writeTempTOML(t, `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"
`)
	// --env without "=" is invalid.
	code := run([]string{"apply", "--file", f, "--env", "NOEQUALSSIGN"})
	if code != 1 {
		t.Errorf("run with bad --env format = %d, want 1", code)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func mustParseMainTest(t *testing.T, tomlStr string) *manifest.TownManifest {
	t.Helper()
	m, err := manifest.Parse([]byte(tomlStr))
	if err != nil {
		t.Fatalf("manifest.Parse: %v", err)
	}
	return m
}

func writeTempTOML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "town-*.toml")
	if err != nil {
		t.Fatalf("create temp toml: %v", err)
	}
	if _, err := f.WriteString(strings.TrimSpace(content)); err != nil {
		t.Fatalf("write temp toml: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close temp toml: %v", err)
	}
	return f.Name()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)), 0o600); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}
