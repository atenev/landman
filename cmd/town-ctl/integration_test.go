//go:build integration

// Package main — integration tests for the custom roles apply pipeline.
//
// These tests require a live Dolt instance reachable via the GT_DOLT_DSN
// environment variable (MySQL-wire DSN, e.g. "root@tcp(127.0.0.1:3306)/gastown").
// They are excluded from the default `go test ./...` run and must be invoked with:
//
//	GT_DOLT_DSN="root@tcp(127.0.0.1:3306)/gastown" go test -tags integration ./cmd/town-ctl/
package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// integrationDSN returns GT_DOLT_DSN or skips the test when it is unset.
func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GT_DOLT_DSN")
	if dsn == "" {
		t.Skip("GT_DOLT_DSN not set; skipping integration test " +
			"(set to a Dolt MySQL DSN, e.g. root@tcp(127.0.0.1:3306)/gastown)")
	}
	return dsn
}

// openIntegrationDB opens a Dolt connection, verifies connectivity, and
// registers t.Cleanup to close it.
func openIntegrationDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Fatalf("ping dolt: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// setupSchema creates all required desired_topology tables (idempotent DDL) and
// truncates every content table so each test starts from an empty desired state.
func setupSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	ddl := []string{
		// desired_topology_versions — must exist before any INSERT referencing it.
		`CREATE TABLE IF NOT EXISTS desired_topology_versions (
			table_name     VARCHAR(128) NOT NULL,
			schema_version INT          NOT NULL,
			written_by     VARCHAR(128),
			written_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (table_name)
		)`,

		// desired_rigs — parent of agent_config, formulas, and rig_custom_roles.
		`CREATE TABLE IF NOT EXISTS desired_rigs (
			name       VARCHAR(128) NOT NULL,
			repo       TEXT         NOT NULL,
			branch     VARCHAR(256) NOT NULL,
			enabled    BOOLEAN      NOT NULL DEFAULT TRUE,
			updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			PRIMARY KEY (name)
		)`,

		// desired_agent_config — one row per (rig, standard-role) pair.
		`CREATE TABLE IF NOT EXISTS desired_agent_config (
			rig_name       VARCHAR(128) NOT NULL,
			role           VARCHAR(64)  NOT NULL,
			enabled        BOOLEAN      NOT NULL DEFAULT TRUE,
			model          VARCHAR(256),
			max_polecats   INT,
			claude_md_path TEXT,
			PRIMARY KEY (rig_name, role),
			CONSTRAINT fk_int_agent_cfg_rig
				FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
					ON DELETE CASCADE ON UPDATE CASCADE,
			CONSTRAINT chk_int_role_enum
				CHECK (role IN ('mayor','witness','refinery','deacon','polecat'))
		)`,

		// desired_formulas — formula schedules per rig.
		`CREATE TABLE IF NOT EXISTS desired_formulas (
			rig_name VARCHAR(128) NOT NULL,
			name     VARCHAR(128) NOT NULL,
			schedule VARCHAR(128) NOT NULL,
			PRIMARY KEY (rig_name, name),
			CONSTRAINT fk_int_formulas_rig
				FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
					ON DELETE CASCADE ON UPDATE CASCADE
		)`,

		// desired_cost_policy — no FK dependency on desired_rigs.
		`CREATE TABLE IF NOT EXISTS desired_cost_policy (
			rig_name     VARCHAR(128)                     NOT NULL,
			budget_type  ENUM('usd','messages','tokens')  NOT NULL,
			daily_budget DECIMAL(16,4)                    NOT NULL,
			warn_at_pct  TINYINT                          NOT NULL DEFAULT 80,
			PRIMARY KEY (rig_name),
			CONSTRAINT chk_int_warn_pct CHECK (warn_at_pct BETWEEN 1 AND 99),
			CONSTRAINT chk_int_budget_pos CHECK (daily_budget > 0)
		)`,

		// desired_custom_roles — one row per [[role]] entry.
		`CREATE TABLE IF NOT EXISTS desired_custom_roles (
			name             VARCHAR(128)                                         NOT NULL,
			description      TEXT,
			scope            ENUM('town','rig')                                   NOT NULL,
			lifespan         ENUM('ephemeral','persistent')                       NOT NULL DEFAULT 'ephemeral',
			trigger_type     ENUM('bead_assigned','schedule','event','manual')    NOT NULL,
			trigger_schedule VARCHAR(64),
			trigger_event    VARCHAR(128),
			claude_md_path   VARCHAR(512)                                         NOT NULL,
			model            VARCHAR(128),
			parent_role      VARCHAR(128)                                         NOT NULL,
			reports_to       VARCHAR(128),
			max_instances    INT                                                  NOT NULL DEFAULT 1,
			PRIMARY KEY (name)
		)`,

		// desired_rig_custom_roles — (rig, role) opt-in junction table.
		// FK → desired_rigs and desired_custom_roles enforce referential integrity.
		`CREATE TABLE IF NOT EXISTS desired_rig_custom_roles (
			rig_name  VARCHAR(128) NOT NULL,
			role_name VARCHAR(128) NOT NULL,
			enabled   BOOLEAN      NOT NULL DEFAULT TRUE,
			PRIMARY KEY (rig_name, role_name),
			CONSTRAINT fk_int_rig_roles_rig
				FOREIGN KEY (rig_name)  REFERENCES desired_rigs(name)
					ON DELETE CASCADE ON UPDATE CASCADE,
			CONSTRAINT fk_int_rig_roles_role
				FOREIGN KEY (role_name) REFERENCES desired_custom_roles(name)
					ON DELETE CASCADE ON UPDATE CASCADE
		)`,
	}

	for _, stmt := range ddl {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("DDL: %v\nSQL: %s", err, stmt)
		}
	}

	// Truncate in FK-safe order (children first, then parents).
	for _, q := range []string{
		"DELETE FROM desired_rig_custom_roles",
		"DELETE FROM desired_custom_roles",
		"DELETE FROM desired_formulas",
		"DELETE FROM desired_agent_config",
		"DELETE FROM desired_cost_policy",
		"DELETE FROM desired_rigs",
		"DELETE FROM desired_topology_versions",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("truncate %q: %v", q, err)
		}
	}
}

// integrationManifest is a town.toml with one custom role (reviewer, scope=rig),
// two rigs where only "backend" opts in, and no secrets/surveyor.
const integrationManifest = `
version = "1"

[town]
name = "test-town"
home = "/opt/gt"

[[role]]
name  = "reviewer"
scope = "rig"

  [role.identity]
  claude_md = "/opt/gt/roles/reviewer/CLAUDE.md"

  [role.trigger]
  type = "bead_assigned"

  [role.supervision]
  parent = "witness"

[[rig]]
name   = "backend"
repo   = "/srv/backend"
branch = "main"

  [rig.agents]
  roles = ["reviewer"]

[[rig]]
name   = "docs"
repo   = "/srv/docs"
branch = "main"
`

func mustParseIntegration(t *testing.T, tomlStr string) *manifest.TownManifest {
	t.Helper()
	m, err := manifest.Parse([]byte(strings.TrimSpace(tomlStr)))
	if err != nil {
		t.Fatalf("manifest.Parse: %v", err)
	}
	return m
}

// countRows returns the number of rows in table matching optional WHERE clause.
func countRows(t *testing.T, db *sql.DB, table, where string) int {
	t.Helper()
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	var n int
	if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("countRows(%s): %v", table, err)
	}
	return n
}

// ── Test 1: Full apply with [[role]] definitions ───────────────────────────────
//
// Verifies:
//   - desired_topology_versions has rows for desired_custom_roles and
//     desired_rig_custom_roles after apply.
//   - desired_custom_roles contains the "reviewer" row with correct fields.
//   - desired_rig_custom_roles has (backend, reviewer) but no row for "docs".
func TestIntegration_FullApply_CustomRoles(t *testing.T) {
	dsn := integrationDSN(t)
	db := openIntegrationDB(t, dsn)
	setupSchema(t, db)

	m := mustParseIntegration(t, integrationManifest)
	if err := applyWrite(m, dsn); err != nil {
		t.Fatalf("applyWrite: %v", err)
	}

	ctx := context.Background()

	// Verify desired_topology_versions rows for both custom-role tables.
	for _, tableName := range []string{"desired_custom_roles", "desired_rig_custom_roles"} {
		var schemaVer int
		row := db.QueryRowContext(ctx,
			"SELECT schema_version FROM desired_topology_versions WHERE table_name = ?", tableName)
		if err := row.Scan(&schemaVer); err != nil {
			t.Errorf("desired_topology_versions[%s]: %v", tableName, err)
			continue
		}
		if schemaVer != 1 {
			t.Errorf("desired_topology_versions[%s].schema_version = %d, want 1", tableName, schemaVer)
		}
	}

	// Verify desired_custom_roles has the correct "reviewer" row.
	var roleName, scope, triggerType, parentRole, lifespan string
	var maxInstances int
	row := db.QueryRowContext(ctx,
		`SELECT name, scope, trigger_type, parent_role, lifespan, max_instances
		 FROM desired_custom_roles WHERE name = 'reviewer'`)
	if err := row.Scan(&roleName, &scope, &triggerType, &parentRole, &lifespan, &maxInstances); err != nil {
		t.Fatalf("SELECT desired_custom_roles[reviewer]: %v", err)
	}
	if roleName != "reviewer" {
		t.Errorf("name = %q, want reviewer", roleName)
	}
	if scope != "rig" {
		t.Errorf("scope = %q, want rig", scope)
	}
	if triggerType != "bead_assigned" {
		t.Errorf("trigger_type = %q, want bead_assigned", triggerType)
	}
	if parentRole != "witness" {
		t.Errorf("parent_role = %q, want witness", parentRole)
	}
	if lifespan != "ephemeral" {
		t.Errorf("lifespan = %q, want ephemeral (default)", lifespan)
	}
	if maxInstances != 1 {
		t.Errorf("max_instances = %d, want 1 (default)", maxInstances)
	}

	// Verify desired_rig_custom_roles: backend opts in, docs does not.
	var rigName string
	row = db.QueryRowContext(ctx,
		"SELECT rig_name FROM desired_rig_custom_roles WHERE rig_name = 'backend' AND role_name = 'reviewer'")
	if err := row.Scan(&rigName); err != nil {
		t.Fatalf("SELECT desired_rig_custom_roles[backend,reviewer]: %v", err)
	}
	if rigName != "backend" {
		t.Errorf("rig_name = %q, want backend", rigName)
	}

	docsCount := countRows(t, db, "desired_rig_custom_roles", "rig_name = 'docs'")
	if docsCount != 0 {
		t.Errorf("docs rig has %d rig_custom_roles rows, want 0", docsCount)
	}
}

// ── Test 2: Idempotent re-apply ───────────────────────────────────────────────
//
// Runs apply twice with the identical manifest. The row counts for
// desired_custom_roles and desired_rig_custom_roles must be identical after
// the second apply — no duplicates, no removals.
func TestIntegration_IdempotentReApply_CustomRoles(t *testing.T) {
	dsn := integrationDSN(t)
	db := openIntegrationDB(t, dsn)
	setupSchema(t, db)

	m1 := mustParseIntegration(t, integrationManifest)
	if err := applyWrite(m1, dsn); err != nil {
		t.Fatalf("first applyWrite: %v", err)
	}

	rolesBefore := countRows(t, db, "desired_custom_roles", "")
	rigRolesBefore := countRows(t, db, "desired_rig_custom_roles", "")

	// Second apply with an identically parsed manifest.
	m2 := mustParseIntegration(t, integrationManifest)
	if err := applyWrite(m2, dsn); err != nil {
		t.Fatalf("second applyWrite: %v", err)
	}

	rolesAfter := countRows(t, db, "desired_custom_roles", "")
	rigRolesAfter := countRows(t, db, "desired_rig_custom_roles", "")

	if rolesBefore != rolesAfter {
		t.Errorf("desired_custom_roles count changed %d → %d on re-apply (want idempotent)",
			rolesBefore, rolesAfter)
	}
	if rigRolesBefore != rigRolesAfter {
		t.Errorf("desired_rig_custom_roles count changed %d → %d on re-apply (want idempotent)",
			rigRolesBefore, rigRolesAfter)
	}

	// Verify the role definition itself is unchanged after the second apply.
	var triggerType string
	row := db.QueryRowContext(context.Background(),
		"SELECT trigger_type FROM desired_custom_roles WHERE name = 'reviewer'")
	if err := row.Scan(&triggerType); err != nil {
		t.Fatalf("SELECT reviewer trigger_type: %v", err)
	}
	if triggerType != "bead_assigned" {
		t.Errorf("trigger_type after re-apply = %q, want bead_assigned", triggerType)
	}
}

// ── Test 3: --dry-run writes zero rows ────────────────────────────────────────
//
// Verifies:
//   - applyDryRun exits with no error (exit code 0).
//   - Structured diff output is printed to stdout and names the role.
//   - Zero rows are written to desired_custom_roles or desired_rig_custom_roles.
func TestIntegration_DryRun_WritesNoRows(t *testing.T) {
	dsn := integrationDSN(t)
	db := openIntegrationDB(t, dsn)
	setupSchema(t, db)

	m := mustParseIntegration(t, integrationManifest)

	// Capture stdout produced by applyDryRun.
	origStdout := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stdout = w

	dryRunErr := applyDryRun(m, dsn)

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("io.Copy stdout: %v", err)
	}
	r.Close()

	if dryRunErr != nil {
		t.Fatalf("applyDryRun returned error: %v", dryRunErr)
	}

	out := buf.String()
	if !strings.Contains(out, "desired_custom_roles") {
		t.Errorf("dry-run output missing 'desired_custom_roles':\n%s", out)
	}
	if !strings.Contains(out, "reviewer") {
		t.Errorf("dry-run output missing role name 'reviewer':\n%s", out)
	}

	// No rows may be written after a dry-run.
	if n := countRows(t, db, "desired_custom_roles", ""); n != 0 {
		t.Errorf("dry-run wrote %d rows to desired_custom_roles, want 0", n)
	}
	if n := countRows(t, db, "desired_rig_custom_roles", ""); n != 0 {
		t.Errorf("dry-run wrote %d rows to desired_rig_custom_roles, want 0", n)
	}
}

// ── Test 4: Transaction rollback ──────────────────────────────────────────────
//
// Injects a fault inside a transaction after inserting into desired_custom_roles
// but before inserting into desired_rig_custom_roles. Rolls back and verifies
// that both tables remain empty (atomicity).
func TestIntegration_TransactionRollback_CustomRoles(t *testing.T) {
	dsn := integrationDSN(t)
	db := openIntegrationDB(t, dsn)
	setupSchema(t, db)

	ctx := context.Background()

	// Pre-insert a rig so FK constraints on desired_rig_custom_roles are satisfiable.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO desired_rigs (name, repo, branch) VALUES ('backend', '/srv/backend', 'main')"); err != nil {
		t.Fatalf("insert rig: %v", err)
	}

	m := mustParseIntegration(t, integrationManifest)
	stmts := townctl.CustomRolesApplySQL(m)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// Execute statements that touch desired_custom_roles (versions upsert and role
	// UPSERTs), stopping just before any statement that touches
	// desired_rig_custom_roles.
	for _, stmt := range stmts {
		if strings.Contains(stmt, "desired_rig_custom_roles") {
			break
		}
		if _, execErr := tx.ExecContext(ctx, stmt); execErr != nil {
			_ = tx.Rollback()
			t.Fatalf("exec: %v\nSQL: %s", execErr, stmt)
		}
	}

	// Inject the fault: roll back without committing.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Both tables must be empty — the rollback was clean.
	if n := countRows(t, db, "desired_custom_roles", ""); n != 0 {
		t.Errorf("desired_custom_roles has %d rows after rollback, want 0", n)
	}
	if n := countRows(t, db, "desired_rig_custom_roles", ""); n != 0 {
		t.Errorf("desired_rig_custom_roles has %d rows after rollback, want 0", n)
	}
}

// ── Test 5: FK constraint enforcement ─────────────────────────────────────────
//
// Attempts to insert a desired_rig_custom_roles row whose role_name does not
// exist in desired_custom_roles. Verifies that Dolt rejects the insert with a
// foreign-key constraint violation.
func TestIntegration_FKConstraint_UndefinedRole(t *testing.T) {
	dsn := integrationDSN(t)
	db := openIntegrationDB(t, dsn)
	setupSchema(t, db)

	ctx := context.Background()

	// A rig must exist for the rig_name FK to pass; role_name is intentionally absent.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO desired_rigs (name, repo, branch) VALUES ('backend', '/srv/backend', 'main')"); err != nil {
		t.Fatalf("insert rig: %v", err)
	}

	_, err := db.ExecContext(ctx,
		"INSERT INTO desired_rig_custom_roles (rig_name, role_name, enabled)"+
			" VALUES ('backend', 'nonexistent-role', TRUE)")
	if err == nil {
		t.Fatal("expected FK violation inserting undefined role_name into desired_rig_custom_roles, got nil")
	}

	errLower := strings.ToLower(err.Error())
	if !strings.Contains(errLower, "foreign key") && !strings.Contains(errLower, "constraint") {
		t.Errorf("expected a foreign-key or constraint error, got: %v", err)
	}
}
