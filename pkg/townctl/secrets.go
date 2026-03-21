// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements secret resolution (ADR-0001, Decision 4): scanning all
// string fields in the resolved manifest for ${VAR} expressions, substituting
// from environment variables and an optional secrets TOML file.
package townctl

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/tenev/dgt/pkg/manifest"
)

var varRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ResolveSecrets resolves all ${VAR} references in string fields of m in-place.
//
// Resolution order (ADR-0001, Decision 4):
//  1. os.Getenv — environment variables take first priority.
//  2. [secrets].file — if set, load the secrets TOML file and look up remaining
//     unresolved vars. Secrets file path itself is env-expanded first.
//
// If any ${VAR} reference remains unresolved after both passes, the function
// returns a non-nil error listing all unresolved names. Secrets are never
// written to Dolt or logged.
func ResolveSecrets(m *manifest.TownManifest) error {
	// Build secrets map from the optional file.
	secretsFromFile := map[string]string{}
	if m.Secrets.File != "" {
		filePath := os.ExpandEnv(m.Secrets.File)
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("secrets: file %s: %w", filePath, err)
		}
		if err := toml.Unmarshal(data, &secretsFromFile); err != nil {
			return fmt.Errorf("secrets: file %s: toml decode: %w", filePath, err)
		}
	}

	lookup := func(name string) (string, bool) {
		if v := os.Getenv(name); v != "" {
			return v, true
		}
		if v, ok := secretsFromFile[name]; ok {
			return v, true
		}
		return "", false
	}

	var unresolved []string

	expand := func(s *string) {
		if s == nil || !strings.Contains(*s, "${") {
			return
		}
		result := varRE.ReplaceAllStringFunc(*s, func(m string) string {
			name := m[2 : len(m)-1]
			if val, ok := lookup(name); ok {
				return val
			}
			unresolved = append(unresolved, "${"+name+"}")
			return m // leave as-is; error reported below
		})
		*s = result
	}

	// Walk all known string fields that may contain ${VAR} references.
	expandManifest(m, expand)

	if len(unresolved) > 0 {
		return fmt.Errorf("unresolved secret references: %s", strings.Join(unresolved, ", "))
	}
	return nil
}

// VerifyRequiredSecrets checks that secrets required at runtime are non-empty
// after resolution. It is called by Apply after ResolveSecrets to catch
// manifests where a required secret was omitted or resolved to an empty string.
//
// Currently enforced:
//   - secrets.anthropic_api_key must be non-empty when town.agents.surveyor=true,
//     because the Surveyor calls the Claude API on every reconciliation cycle.
func VerifyRequiredSecrets(m *manifest.TownManifest) error {
	if m.Town.Agents.Surveyor && m.Secrets.AnthropicAPIKey == "" {
		return fmt.Errorf("secrets.anthropic_api_key: empty after resolution — " +
			"town.agents.surveyor=true requires a non-empty ANTHROPIC_API_KEY " +
			"(set the env-var or configure a secrets file)")
	}
	return nil
}

// BuildSurveyorEnv returns the environment for a Surveyor child process.
// It starts from os.Environ() and injects any resolved secrets that are not
// already present, ensuring secrets sourced from a file (rather than an env-var)
// are forwarded to the agent.
func BuildSurveyorEnv(anthropicAPIKey, githubToken string) []string {
	env := os.Environ()
	if anthropicAPIKey != "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		env = append(env, "ANTHROPIC_API_KEY="+anthropicAPIKey)
	}
	if githubToken != "" && os.Getenv("GITHUB_TOKEN") == "" {
		env = append(env, "GITHUB_TOKEN="+githubToken)
	}
	return env
}

// expandManifest applies fn to every interpolatable string field in m.
func expandManifest(m *manifest.TownManifest, fn func(*string)) {
	fn(&m.Town.Home)
	fn(&m.Town.Agents.SurveyorClaudeMD)
	fn(&m.Town.Agents.SurveyorModel)
	fn(&m.Secrets.AnthropicAPIKey)
	fn(&m.Secrets.GitHubToken)
	fn(&m.Secrets.File)
	fn(&m.Defaults.MayorModel)
	fn(&m.Defaults.PolecatModel)

	for i := range m.Rigs {
		fn(&m.Rigs[i].Repo)
		fn(&m.Rigs[i].Branch)
		fn(&m.Rigs[i].Agents.PolecatModel)
		fn(&m.Rigs[i].Agents.MayorClaudeMD)
	}

	for i := range m.Roles {
		fn(&m.Roles[i].Identity.ClaudeMD)
		fn(&m.Roles[i].Identity.Model)
	}
}
