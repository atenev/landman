package townctl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

func TestResolveSecrets_EnvVar(t *testing.T) {
	t.Setenv("TEST_ANTHROPIC_KEY", "sk-test-123")

	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${TEST_ANTHROPIC_KEY}"
`
	m := parseSecretManifest(t, toml)
	if err := townctl.ResolveSecrets(m); err != nil {
		t.Fatalf("ResolveSecrets: %v", err)
	}
	if m.Secrets.AnthropicAPIKey != "sk-test-123" {
		t.Errorf("AnthropicAPIKey = %q, want %q", m.Secrets.AnthropicAPIKey, "sk-test-123")
	}
}

func TestResolveSecrets_UnresolvedVar_ReturnsError(t *testing.T) {
	os.Unsetenv("MISSING_VAR_XYZ")

	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${MISSING_VAR_XYZ}"
`
	m := parseSecretManifest(t, toml)
	err := townctl.ResolveSecrets(m)
	if err == nil {
		t.Fatal("expected error for unresolved var, got nil")
	}
	if err.Error() == "" {
		t.Error("error message is empty")
	}
}

func TestResolveSecrets_SecretsFile(t *testing.T) {
	// Write a temp secrets TOML file.
	dir := t.TempDir()
	secretsFile := filepath.Join(dir, "secrets.toml")
	if err := os.WriteFile(secretsFile, []byte(`GITHUB_TOKEN = "gh-token-from-file"`), 0o600); err != nil {
		t.Fatal(err)
	}

	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
github_token = "${GITHUB_TOKEN}"
file = "` + secretsFile + `"
`
	m := parseSecretManifest(t, toml)
	if err := townctl.ResolveSecrets(m); err != nil {
		t.Fatalf("ResolveSecrets with file: %v", err)
	}
	if m.Secrets.GitHubToken != "gh-token-from-file" {
		t.Errorf("GitHubToken = %q, want %q", m.Secrets.GitHubToken, "gh-token-from-file")
	}
}

func TestResolveSecrets_EnvTakesPriorityOverFile(t *testing.T) {
	t.Setenv("PRIORITY_TEST_KEY", "from-env")

	dir := t.TempDir()
	secretsFile := filepath.Join(dir, "secrets.toml")
	if err := os.WriteFile(secretsFile, []byte(`PRIORITY_TEST_KEY = "from-file"`), 0o600); err != nil {
		t.Fatal(err)
	}

	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[secrets]
anthropic_api_key = "${PRIORITY_TEST_KEY}"
file = "` + secretsFile + `"
`
	m := parseSecretManifest(t, toml)
	if err := townctl.ResolveSecrets(m); err != nil {
		t.Fatalf("ResolveSecrets: %v", err)
	}
	if m.Secrets.AnthropicAPIKey != "from-env" {
		t.Errorf("env should take priority: got %q, want %q", m.Secrets.AnthropicAPIKey, "from-env")
	}
}

func TestResolveSecrets_NoVars_Noop(t *testing.T) {
	m := parseSecretManifest(t, noPolicy)
	if err := townctl.ResolveSecrets(m); err != nil {
		t.Errorf("no vars: unexpected error: %v", err)
	}
}

func TestVerifyRequiredSecrets_SurveyorWithKey(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"
[town.agents]
surveyor = true

[secrets]
anthropic_api_key = "sk-real-key"
`
	m := parseSecretManifest(t, toml)
	if err := townctl.VerifyRequiredSecrets(m); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifyRequiredSecrets_SurveyorMissingKey(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"
[town.agents]
surveyor = true
`
	m := parseSecretManifest(t, toml)
	err := townctl.VerifyRequiredSecrets(m)
	if err == nil {
		t.Fatal("expected error when surveyor=true and anthropic_api_key is empty, got nil")
	}
}

func TestVerifyRequiredSecrets_NoSurveyorNoKey(t *testing.T) {
	toml := `
version = "1"

[town]
name = "t"
home = "/opt/gt"
`
	m := parseSecretManifest(t, toml)
	if err := townctl.VerifyRequiredSecrets(m); err != nil {
		t.Errorf("no surveyor: unexpected error: %v", err)
	}
}

func TestBuildSurveyorEnv_InjectsFileSourcedKey(t *testing.T) {
	// Ensure the env vars are not set in the test environment.
	// t.Setenv registers cleanup to restore the original value after the test.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GITHUB_TOKEN", "")

	env := townctl.BuildSurveyorEnv("sk-from-file", "gh-from-file")

	var gotKey, gotToken bool
	for _, kv := range env {
		if kv == "ANTHROPIC_API_KEY=sk-from-file" {
			gotKey = true
		}
		if kv == "GITHUB_TOKEN=gh-from-file" {
			gotToken = true
		}
	}
	if !gotKey {
		t.Error("ANTHROPIC_API_KEY not injected into surveyor env")
	}
	if !gotToken {
		t.Error("GITHUB_TOKEN not injected into surveyor env")
	}
}

func TestBuildSurveyorEnv_SkipsAlreadySetKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")

	env := townctl.BuildSurveyorEnv("sk-from-file", "")

	var count int
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			count++
		}
	}
	// Should appear exactly once (from os.Environ(), not injected again).
	if count != 1 {
		t.Errorf("ANTHROPIC_API_KEY appears %d times in env, want 1", count)
	}
}

// parseSecretManifest parses without validating apply-time paths so secrets
// tests don't need real CLAUDE.md files on disk.
func parseSecretManifest(t *testing.T, tomlStr string) *manifest.TownManifest {
	t.Helper()
	m, err := manifest.Parse([]byte(tomlStr))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return m
}
