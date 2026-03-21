// Package townctl_test — integration tests verifying Dolt enforces CHECK
// constraints defined in migrations 001, 002, 004, 005, and 007.
//
// Background: MySQL 5.7 parses CHECK constraints but silently ignores them.
// MySQL 8.0+ and recent Dolt (≥ v1.0) enforce them. These tests guard against
// silent constraint bypass by attempting an invalid INSERT for each CHECK and
// asserting that Dolt rejects it.
//
// The tests are skipped unless TOWN_CTL_TEST_DOLT_HOST is set and all
// migrations (001–007) have been applied. See apply_integ_test.go for
// environment variable documentation and the doltIntegSkip helper.
package townctl_test

import (
	"fmt"
	"strings"
	"testing"
)

// ── dolt version check ────────────────────────────────────────────────────────

// TestDoltInteg_DoltVersion logs the Dolt server version and verifies it
// returns a non-empty string. Use this output in CI to confirm whether the
// Dolt version in use enforces CHECK constraints.
//
// Dolt has enforced CHECK constraints since v1.0.0. If tests below FAIL with
// "INSERT succeeded — CHECK constraint is NOT being enforced", the version
// logged here can be used to diagnose the issue.
func TestDoltInteg_DoltVersion(t *testing.T) {
	db := doltIntegSkip(t)

	var version string
	if err := db.QueryRow("SELECT @@version").Scan(&version); err != nil {
		t.Fatalf("SELECT @@version: %v", err)
	}
	if version == "" {
		t.Fatal("Dolt returned an empty version string")
	}
	t.Logf("Dolt server @@version: %s", version)

	// dolt_version() returns the Dolt-specific semver (distinct from MySQL compat version).
	var doltVer string
	if err := db.QueryRow("SELECT dolt_version()").Scan(&doltVer); err == nil {
		t.Logf("dolt_version(): %s", doltVer)
	}
}

// ── migration 001: desired_agent_config — chk_role_enum ──────────────────────

// TestDoltInteg_CHK_RoleEnum verifies that Dolt enforces chk_role_enum on
// desired_agent_config.role. Only the five valid role literals are accepted;
// any other value must be rejected.
func TestDoltInteg_CHK_RoleEnum(t *testing.T) {
	db := doltIntegSkip(t)

	_, _ = db.Exec("INSERT IGNORE INTO desired_rigs (name, repo, branch) VALUES ('chk-role-rig', '/tmp/r', 'main')")
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM desired_agent_config WHERE rig_name = 'chk-role-rig'")
		_, _ = db.Exec("DELETE FROM desired_rigs WHERE name = 'chk-role-rig'")
	})

	for _, role := range []string{"unknown_role", "", "MAYOR", "Mayor"} {
		role := role
		t.Run(fmt.Sprintf("role=%q", role), func(t *testing.T) {
			_, err := db.Exec(
				"INSERT INTO desired_agent_config (rig_name, role, enabled)"+
					" VALUES ('chk-role-rig', ?, TRUE)",
				role,
			)
			if err == nil {
				t.Fatalf("chk_role_enum: INSERT with role=%q succeeded — constraint not enforced", role)
			}
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "check") &&
				!strings.Contains(errMsg, "constraint") &&
				!strings.Contains(errMsg, "enum") {
				t.Errorf("expected CHECK/constraint/enum error for role=%q, got: %v", role, err)
			}
		})
	}
}

// ── migration 002: desired_custom_roles — chk_custom_role_name_not_builtin ───

// TestDoltInteg_CHK_CustomRoleNameNotBuiltin verifies that Dolt enforces
// chk_custom_role_name_not_builtin on desired_custom_roles.name. All seven
// built-in role names must be rejected.
func TestDoltInteg_CHK_CustomRoleNameNotBuiltin(t *testing.T) {
	db := doltIntegSkip(t)

	for _, name := range []string{"mayor", "polecat", "witness", "refinery", "deacon", "dog", "crew"} {
		name := name
		t.Run(fmt.Sprintf("name=%s", name), func(t *testing.T) {
			t.Cleanup(func() {
				_, _ = db.Exec("DELETE FROM desired_custom_roles WHERE name = ?", name)
			})
			_, err := db.Exec(
				"INSERT INTO desired_custom_roles"+
					" (name, scope, lifespan, trigger_type, claude_md_path, parent_role, max_instances)"+
					" VALUES (?, 'rig', 'ephemeral', 'manual', '/tmp/x.md', 'witness', 1)",
				name,
			)
			if err == nil {
				t.Fatalf("chk_custom_role_name_not_builtin: INSERT with builtin name=%q succeeded — constraint not enforced", name)
			}
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "check") && !strings.Contains(errMsg, "constraint") {
				t.Errorf("expected CHECK/constraint error for name=%q, got: %v", name, err)
			}
		})
	}
}

// TestDoltInteg_CHK_Trigger verifies that Dolt enforces chk_trigger on
// desired_custom_roles. A schedule trigger without trigger_schedule, and an
// event trigger without trigger_event, must both fail.
func TestDoltInteg_CHK_Trigger(t *testing.T) {
	db := doltIntegSkip(t)

	cases := []struct {
		name        string
		triggerType string
	}{
		{"schedule_without_schedule_expr", "schedule"},
		{"event_without_event_name", "event"},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			roleName := "chk-trigger-test-" + c.name
			t.Cleanup(func() {
				_, _ = db.Exec("DELETE FROM desired_custom_roles WHERE name = ?", roleName)
			})
			_, err := db.Exec(
				"INSERT INTO desired_custom_roles"+
					" (name, scope, lifespan, trigger_type, trigger_schedule, trigger_event,"+
					"  claude_md_path, parent_role, max_instances)"+
					" VALUES (?, 'rig', 'ephemeral', ?, NULL, NULL, '/tmp/x.md', 'witness', 1)",
				roleName, c.triggerType,
			)
			if err == nil {
				t.Fatalf("chk_trigger: INSERT with trigger_type=%q and NULL trigger fields succeeded — constraint not enforced",
					c.triggerType)
			}
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "check") && !strings.Contains(errMsg, "constraint") {
				t.Errorf("expected CHECK/constraint error for %s, got: %v", c.name, err)
			}
		})
	}
}

// ── migration 004: desired_cost_policy ───────────────────────────────────────

// TestDoltInteg_CHK_WarnAtPctRange verifies that Dolt enforces
// chk_warn_at_pct_range (BETWEEN 1 AND 99) on desired_cost_policy.warn_at_pct.
func TestDoltInteg_CHK_WarnAtPctRange(t *testing.T) {
	db := doltIntegSkip(t)

	_, _ = db.Exec("INSERT IGNORE INTO desired_rigs (name, repo, branch) VALUES ('chk-cost-rig', '/tmp/r', 'main')")
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM desired_cost_policy WHERE rig_name = 'chk-cost-rig'")
		_, _ = db.Exec("DELETE FROM desired_rigs WHERE name = 'chk-cost-rig'")
	})

	for _, pct := range []int{0, 100, -1, 101} {
		pct := pct
		t.Run(fmt.Sprintf("pct=%d", pct), func(t *testing.T) {
			_, err := db.Exec(
				"INSERT INTO desired_cost_policy (rig_name, budget_type, daily_budget, warn_at_pct)"+
					" VALUES ('chk-cost-rig', 'usd', 100.0, ?)",
				pct,
			)
			if err == nil {
				t.Fatalf("chk_warn_at_pct_range: INSERT with warn_at_pct=%d succeeded — constraint not enforced", pct)
			}
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "check") && !strings.Contains(errMsg, "constraint") {
				t.Errorf("expected CHECK/constraint error for warn_at_pct=%d, got: %v", pct, err)
			}
		})
	}
}

// TestDoltInteg_CHK_DailyBudgetPositive verifies that Dolt enforces
// chk_daily_budget_positive (daily_budget > 0) on desired_cost_policy.
func TestDoltInteg_CHK_DailyBudgetPositive(t *testing.T) {
	db := doltIntegSkip(t)

	_, _ = db.Exec("INSERT IGNORE INTO desired_rigs (name, repo, branch) VALUES ('chk-budget-rig', '/tmp/r', 'main')")
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM desired_cost_policy WHERE rig_name = 'chk-budget-rig'")
		_, _ = db.Exec("DELETE FROM desired_rigs WHERE name = 'chk-budget-rig'")
	})

	for _, budget := range []float64{0, -0.01, -100} {
		budget := budget
		t.Run(fmt.Sprintf("budget=%.2f", budget), func(t *testing.T) {
			_, err := db.Exec(
				"INSERT INTO desired_cost_policy (rig_name, budget_type, daily_budget, warn_at_pct)"+
					" VALUES ('chk-budget-rig', 'usd', ?, 80)",
				budget,
			)
			if err == nil {
				t.Fatalf("chk_daily_budget_positive: INSERT with daily_budget=%v succeeded — constraint not enforced", budget)
			}
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "check") && !strings.Contains(errMsg, "constraint") {
				t.Errorf("expected CHECK/constraint error for daily_budget=%v, got: %v", budget, err)
			}
		})
	}
}

// ── migration 005: actual_custom_roles — chk_town_sentinel ───────────────────

// TestDoltInteg_CHK_TownSentinel verifies that Dolt enforces chk_town_sentinel
// (rig_name != '') on actual_custom_roles. The empty string is not allowed;
// use '__town__' for town-scoped roles.
func TestDoltInteg_CHK_TownSentinel(t *testing.T) {
	db := doltIntegSkip(t)

	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM actual_custom_roles WHERE rig_name = ''")
	})

	_, err := db.Exec(
		"INSERT INTO actual_custom_roles (rig_name, role_name, instance_index, status)"+
			" VALUES ('', 'some-role', 0, 'running')",
	)
	if err == nil {
		t.Fatal("chk_town_sentinel: INSERT with empty rig_name succeeded — constraint not enforced\n" +
			"rig_name='' is not allowed; use '__town__' for town-scoped roles (migration 005)")
	}
	errMsg := strings.ToLower(err.Error())
	if !strings.Contains(errMsg, "check") && !strings.Contains(errMsg, "constraint") {
		t.Errorf("expected CHECK/constraint error for empty rig_name, got: %v", err)
	}
}

// ── migration 007: desired_topology_lock — chk_singleton ─────────────────────

// TestDoltInteg_CHK_Singleton verifies that Dolt enforces chk_singleton
// (singleton = 'X') on desired_topology_lock. Any other value must be rejected.
func TestDoltInteg_CHK_Singleton(t *testing.T) {
	db := doltIntegSkip(t)

	for _, s := range []string{"Y", "x", " ", "XX"} {
		s := s
		t.Run(fmt.Sprintf("singleton=%q", s), func(t *testing.T) {
			t.Cleanup(func() {
				_, _ = db.Exec("DELETE FROM desired_topology_lock WHERE singleton = ?", s)
			})
			_, err := db.Exec(
				"INSERT INTO desired_topology_lock (singleton, holder, acquired_at)"+
					" VALUES (?, 'test-holder', NOW())",
				s,
			)
			if err == nil {
				t.Fatalf("chk_singleton: INSERT with singleton=%q succeeded — constraint not enforced", s)
			}
			errMsg := strings.ToLower(err.Error())
			if !strings.Contains(errMsg, "check") && !strings.Contains(errMsg, "constraint") {
				t.Errorf("expected CHECK/constraint error for singleton=%q, got: %v", s, err)
			}
		})
	}
}
