package townctl_test

import (
	"os"
	"path/filepath"
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
