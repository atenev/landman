package manifest

import (
	"fmt"
	"os"
	"strings"
)

// mergedClaudeMDSeparator is the section header written between base and
// override content in a merged CLAUDE.md file (ADR-0005).
const mergedClaudeMDSeparator = "\n\n---\n<!-- extends override -->\n\n"

// MergeClaudeMDFiles reads basePath and overridePath, concatenates their
// contents with a separator, and returns the merged string. Both paths must
// exist and be readable.
//
// Merge semantics (ADR-0005, Decision 1): string concatenation with a
// human-readable separator. The base content appears first so that base
// instructions provide the foundational context; the override content
// follows and takes priority when the agent encounters conflicting guidance.
func MergeClaudeMDFiles(basePath, overridePath string) (string, error) {
	base, err := os.ReadFile(basePath)
	if err != nil {
		return "", fmt.Errorf("merge: read base %s: %w", basePath, err)
	}
	override, err := os.ReadFile(overridePath)
	if err != nil {
		return "", fmt.Errorf("merge: read override %s: %w", overridePath, err)
	}
	var b strings.Builder
	b.Write(base)
	b.WriteString(mergedClaudeMDSeparator)
	b.Write(override)
	return b.String(), nil
}

// ResolveExtendsChain returns the ordered list of claude_md paths for role
// name, starting from the root base role down to name itself. The list has
// length 1 when the role does not extend anything, and length N for a chain
// of N-1 extends hops.
//
// The caller must have already validated the manifest (no cycles, all
// references resolved) before calling this function.
func ResolveExtendsChain(name string, roles []RoleSpec) ([]string, error) {
	byName := make(map[string]RoleSpec, len(roles))
	for _, r := range roles {
		byName[r.Name] = r
	}
	return resolveChain(name, byName, len(roles)+1)
}

func resolveChain(name string, byName map[string]RoleSpec, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		return nil, fmt.Errorf("extends chain for %q exceeds maximum depth (cycle?)", name)
	}
	role, ok := byName[name]
	if !ok {
		return nil, fmt.Errorf("role %q not found", name)
	}
	if role.Identity.Extends == "" {
		return []string{os.ExpandEnv(role.Identity.ClaudeMD)}, nil
	}
	base, err := resolveChain(role.Identity.Extends, byName, maxDepth-1)
	if err != nil {
		return nil, err
	}
	return append(base, os.ExpandEnv(role.Identity.ClaudeMD)), nil
}

// MergeExtendsChain merges all CLAUDE.md files in chain order, concatenating
// them with mergedClaudeMDSeparator between each pair. chain must contain at
// least one path. For a chain of length 1 (no extends), the single file is
// read and returned unchanged.
func MergeExtendsChain(chain []string) (string, error) {
	if len(chain) == 0 {
		return "", fmt.Errorf("MergeExtendsChain: empty chain")
	}
	first, err := os.ReadFile(chain[0])
	if err != nil {
		return "", fmt.Errorf("merge: read %s: %w", chain[0], err)
	}
	result := string(first)
	for _, path := range chain[1:] {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("merge: read %s: %w", path, err)
		}
		result += mergedClaudeMDSeparator + string(content)
	}
	return result, nil
}
