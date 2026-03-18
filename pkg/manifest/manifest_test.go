package manifest_test

import (
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/manifest"
)

const minimalValid = `
version = "1"

[town]
name      = "my-town"
home      = "/opt/gt"
dolt_port = 3306

[[rig]]
name    = "backend"
repo    = "/srv/repos/backend"
branch  = "main"
enabled = true

  [rig.agents]
  mayor    = true
  witness  = true
  refinery = true
  deacon   = true
`

func TestParse_MinimalValid(t *testing.T) {
	m, err := manifest.Parse([]byte(minimalValid))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Version != "1" {
		t.Errorf("version = %q, want %q", m.Version, "1")
	}
	if m.Town.Name != "my-town" {
		t.Errorf("town.name = %q, want %q", m.Town.Name, "my-town")
	}
	if len(m.Rigs) != 1 || m.Rigs[0].Name != "backend" {
		t.Errorf("unexpected rigs: %+v", m.Rigs)
	}
}

func TestParse_WrongVersion(t *testing.T) {
	bad := strings.ReplaceAll(minimalValid, `version = "1"`, `version = "2"`)
	if _, err := manifest.Parse([]byte(bad)); err == nil {
		t.Fatal("expected error for version=2, got nil")
	}
}

func TestParse_DuplicateRigNames(t *testing.T) {
	dup := minimalValid + `
[[rig]]
name   = "backend"
repo   = "/srv/repos/other"
branch = "main"
`
	if _, err := manifest.Parse([]byte(dup)); err == nil {
		t.Fatal("expected error for duplicate rig name, got nil")
	}
}

func TestParse_WitnessRequiresMayor(t *testing.T) {
	bad := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [rig.agents]
  mayor   = false
  witness = true
`
	if _, err := manifest.Parse([]byte(bad)); err == nil {
		t.Fatal("expected error for witness=true without mayor=true, got nil")
	}
}

func TestParse_MaxPolecastsOverLimit(t *testing.T) {
	bad := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [rig.agents]
  max_polecats = 31
`
	if _, err := manifest.Parse([]byte(bad)); err == nil {
		t.Fatal("expected error for max_polecats=31, got nil")
	}
}

func TestParse_InvalidCronSchedule(t *testing.T) {
	bad := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [[rig.formula]]
  name     = "nightly"
  schedule = "not-a-cron"
`
	if _, err := manifest.Parse([]byte(bad)); err == nil {
		t.Fatal("expected error for invalid cron schedule, got nil")
	}
}

func TestParse_ValidCronSchedule(t *testing.T) {
	good := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "r"
repo   = "/srv/r"
branch = "main"

  [[rig.formula]]
  name     = "nightly-tests"
  schedule = "0 2 * * *"
`
	if _, err := manifest.Parse([]byte(good)); err != nil {
		t.Fatalf("unexpected error for valid cron: %v", err)
	}
}

func TestParse_InvalidRigSlug(t *testing.T) {
	bad := `
version = "1"

[town]
name = "t"
home = "/opt/gt"

[[rig]]
name   = "My Backend"
repo   = "/srv/r"
branch = "main"
`
	if _, err := manifest.Parse([]byte(bad)); err == nil {
		t.Fatal("expected error for invalid slug, got nil")
	}
}

func TestParse_FullManifest(t *testing.T) {
	full := `
version = "1"

[town]
name      = "prod"
home      = "/opt/gt"
dolt_port = 3306

  [town.agents]
  surveyor = true

[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20

[secrets]
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"

includes = ["./rigs/*.toml"]

[[rig]]
name    = "backend"
repo    = "${PROJECTS_DIR}/backend"
branch  = "main"
enabled = true

  [rig.agents]
  mayor         = true
  witness       = true
  refinery      = true
  deacon        = true
  max_polecats  = 30
  polecat_model = "claude-haiku-4-5-20251001"

  [[rig.formula]]
  name     = "nightly-tests"
  schedule = "0 2 * * *"
`
	m, err := manifest.Parse([]byte(full))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.Town.Agents.Surveyor {
		t.Error("expected town.agents.surveyor = true")
	}
	if m.Defaults.MaxPolecats != 20 {
		t.Errorf("defaults.max_polecats = %d, want 20", m.Defaults.MaxPolecats)
	}
	if len(m.Rigs[0].Formulas) != 1 {
		t.Errorf("expected 1 formula, got %d", len(m.Rigs[0].Formulas))
	}
}
