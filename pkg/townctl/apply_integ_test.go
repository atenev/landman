// Package townctl_test — integration tests for the custom roles apply pipeline
// (dgt-6jy: tasks 6.6-6.8).
//
// Dry-run tests (no Dolt required) run unconditionally.
//
// Dolt-dependent tests are skipped unless the following environment variables
// are set:
//
//	TOWN_CTL_TEST_DOLT_HOST  (default: skip)
//	TOWN_CTL_TEST_DOLT_PORT  (default: 3306)
//	TOWN_CTL_TEST_DOLT_DB
//	TOWN_CTL_TEST_DOLT_USER
//	TOWN_CTL_TEST_DOLT_PASSWORD  (optional)
//
// The Dolt database must have migration 002 applied so that
// desired_custom_roles, desired_rig_custom_roles, and
// desired_topology_versions tables exist.
package townctl_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tenev/dgt/pkg/townctl"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// doltIntegSkip skips t unless the TOWN_CTL_TEST_DOLT_HOST env var is set.
// It returns a live *townctl.DB on success.
func doltIntegSkip(t *testing.T) *townctl.DB {
	t.Helper()
	host := os.Getenv("TOWN_CTL_TEST_DOLT_HOST")
	if host == "" {
		t.Skip("TOWN_CTL_TEST_DOLT_HOST not set — skipping Dolt integration test")
	}
	port := 3306
	if p := os.Getenv("TOWN_CTL_TEST_DOLT_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("invalid TOWN_CTL_TEST_DOLT_PORT %q: %v", p, err)
		}
		port = v
	}
	dbName := os.Getenv("TOWN_CTL_TEST_DOLT_DB")
	if dbName == "" {
		t.Fatal("TOWN_CTL_TEST_DOLT_DB must be set when TOWN_CTL_TEST_DOLT_HOST is set")
	}
	user := os.Getenv("TOWN_CTL_TEST_DOLT_USER")
	if user == "" {
		t.Fatal("TOWN_CTL_TEST_DOLT_USER must be set when TOWN_CTL_TEST_DOLT_HOST is set")
	}
	password := os.Getenv("TOWN_CTL_TEST_DOLT_PASSWORD")

	db, err := townctl.Connect(t.Context(), host, port, dbName, user, password)
	if err != nil {
		t.Fatalf("doltIntegSkip: Connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// writeTempManifest writes a manifest TOML file to dir and returns its path.
// claudeMDPath is interpolated into role.identity.claude_md so that
// manifest.ValidateApplyTime can find the file.
func writeTempManifest(t *testing.T, dir string, roles bool, claudeMDPath string) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString(`version = "1"

[town]
name = "integ-test"
home = "/opt/gt"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"
`)
	if roles {
		fmt.Fprintf(&buf, `
[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = %q

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

`, claudeMDPath)
		// backend rig opts in to reviewer.
		buf.WriteString(`
[rig.agents]
roles = ["reviewer"]
`)
	}
	path := filepath.Join(dir, "town.toml")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writeTempManifest: %v", err)
	}
	return path
}

// captureStdout redirects os.Stdout for the duration of fn and returns the
// bytes written. Must not be called concurrently with other stdout users.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureStdout: os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("captureStdout: io.Copy: %v", err)
	}
	r.Close()
	return buf.String()
}

// ── dry-run tests (no Dolt) ───────────────────────────────────────────────────

// TestApplyDryRun_ExitCodeZero verifies that Apply with DryRun=true returns a
// nil error (exit code 0) even when no Dolt server is configured.
func TestApplyDryRun_ExitCodeZero(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTempManifest(t, dir, true, claudePath)

	err := townctl.Apply(manifestPath, townctl.ApplyOptions{DryRun: true})
	if err != nil {
		t.Errorf("Apply(DryRun=true) returned non-nil error: %v", err)
	}
}

// TestApplyDryRun_PrintsCustomRolesDiff verifies that --dry-run prints a
// structured diff for [[role]] definitions to stdout (+/~/- prefix convention).
func TestApplyDryRun_PrintsCustomRolesDiff(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTempManifest(t, dir, true, claudePath)

	out := captureStdout(t, func() {
		if err := townctl.Apply(manifestPath, townctl.ApplyOptions{DryRun: true}); err != nil {
			t.Errorf("Apply(DryRun=true): %v", err)
		}
	})

	// The dry-run formatter uses "+ desired_custom_roles: ..." for add ops.
	if !strings.Contains(out, "+ desired_custom_roles") {
		t.Errorf("expected '+ desired_custom_roles' in dry-run output; got:\n%s", out)
	}
	if !strings.Contains(out, "reviewer") {
		t.Errorf("expected role name 'reviewer' in dry-run output; got:\n%s", out)
	}
}

// TestApplyDryRun_NoDoltConnection verifies that --dry-run does not attempt a
// Dolt connection: even an impossible host must not cause failure.
func TestApplyDryRun_NoDoltConnection(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTempManifest(t, dir, true, claudePath)

	opts := townctl.ApplyOptions{
		DryRun:   true,
		DoltHost: "impossible-nonexistent-host-99999",
		DoltPort: 1,
	}
	err := townctl.Apply(manifestPath, opts)
	if err != nil {
		t.Errorf("Apply(DryRun=true) with unreachable host returned error: %v", err)
	}
}

// TestApplyDryRun_NoRoles_PrintsNoChanges verifies that --dry-run with a
// manifest containing no [[role]] blocks reports no changes for custom roles.
func TestApplyDryRun_NoRoles_PrintsNoChanges(t *testing.T) {
	dir := t.TempDir()
	manifestPath := writeTempManifest(t, dir, false, "")

	out := captureStdout(t, func() {
		if err := townctl.Apply(manifestPath, townctl.ApplyOptions{DryRun: true}); err != nil {
			t.Errorf("Apply(DryRun=true, no roles): %v", err)
		}
	})

	if !strings.Contains(out, "no changes") {
		t.Errorf("expected 'no changes' in output for manifest with no roles; got:\n%s", out)
	}
}

// TestApplyDryRun_RigOptIn_PrintsJunctionAdd verifies that --dry-run shows
// a '+' entry for desired_rig_custom_roles when a rig opts in to a role.
func TestApplyDryRun_RigOptIn_PrintsJunctionAdd(t *testing.T) {
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	// writeTempManifest with roles=true already adds a rig opt-in for "reviewer".
	manifestPath := writeTempManifest(t, dir, true, claudePath)

	out := captureStdout(t, func() {
		_ = townctl.Apply(manifestPath, townctl.ApplyOptions{DryRun: true})
	})

	if !strings.Contains(out, "+ desired_rig_custom_roles") {
		t.Errorf("expected '+ desired_rig_custom_roles' in dry-run output; got:\n%s", out)
	}
	if !strings.Contains(out, "backend") {
		t.Errorf("expected rig name 'backend' in dry-run output; got:\n%s", out)
	}
}

// ── Dolt integration tests ────────────────────────────────────────────────────

// TestDoltInteg_FullApply_CustomRoles applies a manifest with one [[role]] and
// one rig opt-in, then verifies that all three tables contain the expected rows.
func TestDoltInteg_FullApply_CustomRoles(t *testing.T) {
	db := doltIntegSkip(t)
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTempManifest(t, dir, true, claudePath)

	opts := doltApplyOpts(t)
	if err := townctl.Apply(manifestPath, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// desired_topology_versions must contain a row for desired_custom_roles.
	var count int
	row := db.QueryRow(
		"SELECT COUNT(*) FROM desired_topology_versions WHERE table_name = 'desired_custom_roles'",
	)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query desired_topology_versions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in desired_topology_versions for desired_custom_roles, got %d", count)
	}

	// desired_custom_roles must have the 'reviewer' row.
	row = db.QueryRow("SELECT COUNT(*) FROM desired_custom_roles WHERE name = 'reviewer'")
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query desired_custom_roles: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in desired_custom_roles for 'reviewer', got %d", count)
	}

	// desired_rig_custom_roles must have the (backend, reviewer) opt-in.
	row = db.QueryRow(
		"SELECT COUNT(*) FROM desired_rig_custom_roles WHERE rig_name = 'backend' AND role_name = 'reviewer'",
	)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query desired_rig_custom_roles: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row in desired_rig_custom_roles for (backend, reviewer), got %d", count)
	}
}

// TestDoltInteg_Idempotent_CustomRoles applies the same manifest twice and
// verifies that the second apply produces no new Dolt commit (no diff).
func TestDoltInteg_Idempotent_CustomRoles(t *testing.T) {
	db := doltIntegSkip(t)
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte("# Reviewer"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeTempManifest(t, dir, true, claudePath)
	opts := doltApplyOpts(t)

	// First apply.
	if err := townctl.Apply(manifestPath, opts); err != nil {
		t.Fatalf("first Apply: %v", err)
	}

	// Record current Dolt HEAD commit hash.
	var hashBefore string
	if err := db.QueryRow("SELECT @@dolt_HEAD").Scan(&hashBefore); err != nil {
		t.Fatalf("read dolt_HEAD before second apply: %v", err)
	}

	// Second apply — identical manifest.
	if err := townctl.Apply(manifestPath, opts); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	// Dolt HEAD should be unchanged: no diff means no new commit.
	var hashAfter string
	if err := db.QueryRow("SELECT @@dolt_HEAD").Scan(&hashAfter); err != nil {
		t.Fatalf("read dolt_HEAD after second apply: %v", err)
	}
	if hashBefore != hashAfter {
		t.Errorf("second apply created a new Dolt commit (not idempotent): before=%s after=%s",
			hashBefore, hashAfter)
	}
}

// TestDoltInteg_TransactionRollback verifies that a fault mid-transaction causes
// both the desired_custom_roles write and the subsequent desired_rig_custom_roles
// write to be rolled back atomically.
func TestDoltInteg_TransactionRollback(t *testing.T) {
	db := doltIntegSkip(t)

	// Craft a statement list that:
	//   1. Upserts a sentinel role into desired_custom_roles.
	//   2. Executes an intentionally bad SQL statement (syntax error).
	//   3. Would upsert desired_rig_custom_roles (never reached).
	const sentinelRole = "rollback-test-sentinel-99"
	stmts := []townctl.Stmt{
		{
			Query: "INSERT INTO desired_custom_roles" +
				" (name, scope, lifespan, trigger_type, claude_md_path, parent_role, max_instances)" +
				" VALUES (?, 'rig', 'ephemeral', 'manual', '/tmp/x.md', 'witness', 1)" +
				" ON DUPLICATE KEY UPDATE scope = VALUES(scope);",
			Args: []any{sentinelRole},
		},
		// Bad statement — causes rollback.
		{Query: "THIS IS NOT VALID SQL !!!;"},
	}

	err := db.ExecTransaction(stmts)
	if err == nil {
		t.Fatal("expected ExecTransaction to return an error for invalid SQL, got nil")
	}

	// After rollback, the sentinel role must NOT be present.
	var count int
	row := db.QueryRow(
		fmt.Sprintf("SELECT COUNT(*) FROM desired_custom_roles WHERE name = '%s'", sentinelRole),
	)
	if scanErr := row.Scan(&count); scanErr != nil {
		t.Fatalf("query after rollback: %v", scanErr)
	}
	if count != 0 {
		t.Errorf("rollback failed: sentinel row '%s' is present in desired_custom_roles after error",
			sentinelRole)
	}
}

// TestDoltInteg_FKConstraint_DesiredRigCustomRoles verifies that Dolt enforces
// the FK from desired_rig_custom_roles.role_name → desired_custom_roles.name.
// Attempting to insert a rig opt-in for an undefined role must fail.
func TestDoltInteg_FKConstraint_DesiredRigCustomRoles(t *testing.T) {
	db := doltIntegSkip(t)

	const undefinedRole = "fk-test-nonexistent-role-99999"
	err := db.ExecTransaction([]townctl.Stmt{
		{
			Query: "INSERT INTO desired_rig_custom_roles (rig_name, role_name, enabled)" +
				" VALUES ('backend', ?, TRUE);",
			Args: []any{undefinedRole},
		},
	})
	if err == nil {
		t.Fatal("expected FK violation error when inserting undefined role_name, got nil")
	}

	// The error message from Dolt/MySQL FK violations typically includes "foreign key".
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "foreign key") && !strings.Contains(errMsg, "constraint") {
		t.Errorf("expected FK/constraint error message, got: %v", err)
	}
}

// doltApplyOpts builds ApplyOptions from env vars (host/port/db/user/password).
// Must only be called after doltIntegSkip has confirmed the env vars are set.
func doltApplyOpts(t *testing.T) townctl.ApplyOptions {
	t.Helper()
	port := 3306
	if p := os.Getenv("TOWN_CTL_TEST_DOLT_PORT"); p != "" {
		v, err := strconv.Atoi(p)
		if err == nil {
			port = v
		}
	}
	return townctl.ApplyOptions{
		DoltHost:     os.Getenv("TOWN_CTL_TEST_DOLT_HOST"),
		DoltPort:     port,
		DoltDB:       os.Getenv("TOWN_CTL_TEST_DOLT_DB"),
		DoltUser:     os.Getenv("TOWN_CTL_TEST_DOLT_USER"),
		DoltPassword: os.Getenv("TOWN_CTL_TEST_DOLT_PASSWORD"),
	}
}
