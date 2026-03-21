package main

import (
	"os"
	"strings"
	"testing"
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
	// With --dry-run, town-ctl should print the plan and exit 0 without
	// attempting a Dolt connection.
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

// ── --env overlay ───────────────────────────────────────────────────────────

func TestStringSlice_LastEnvWins(t *testing.T) {
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
	// Verify multiple --env flags are accepted (dry-run to avoid Dolt).
	code := run([]string{
		"apply", "--file", f,
		"--env", "TEST_OVERLAY_VAR=first-value",
		"--env", "TEST_OVERLAY_VAR=last-wins",
		"--dry-run",
		"--dolt-dsn", "root@tcp(127.0.0.1:1)/gastown",
	})
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
