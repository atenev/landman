// town-ctl is the Gas Town topology actuator. It reads a town.toml manifest
// and writes the desired topology to a Dolt instance.
//
// Usage:
//
//	town-ctl apply [--file town.toml] [--dry-run] [--env NAME=VALUE...]
//	               [--dolt-dsn DSN]
//	town-ctl version
//
// The apply command:
//  1. Parses and validates town.toml (JSON Schema + go-validator).
//  2. Resolves [[includes]] glob patterns → merges overlay fragments.
//  3. Expands ${VAR} interpolations in all path fields.
//  4. Resolves secrets (env-var refs, optional secrets file).
//  5. Diffs desired state against current Dolt rows.
//  6. With --dry-run: prints a structured diff and exits 0 (or 1 on error).
//  7. Without --dry-run: writes an atomic Dolt transaction; idempotent on no-op.
//  8. Checks [town.agents].surveyor and launches/verifies the process.
//
// Exit codes: 0 = success, 1 = error (human-readable message on stderr).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	// Register the MySQL driver used for Dolt's MySQL-compatible endpoint.
	_ "github.com/go-sql-driver/mysql"
	toml "github.com/pelletier/go-toml/v2"

	"github.com/tenev/dgt/pkg/manifest"
	"github.com/tenev/dgt/pkg/townctl"
)

func main() {
	// Configure structured logging to stderr for all diagnostic output.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		slog.Error("missing command", "usage", "town-ctl <apply|version> [flags]")
		return 1
	}
	switch args[0] {
	case "apply":
		if err := applyCmd(args[1:]); err != nil {
			slog.Error("apply failed", "error", err.Error())
			return 1
		}
		return 0
	case "version":
		fmt.Printf("%s\n", townctl.BinaryVersion)
		return 0
	default:
		slog.Error("unknown command", "command", args[0], "usage", "town-ctl <apply|version> [flags]")
		return 1
	}
}

type applyFlags struct {
	file    string
	dryRun  bool
	doltDSN string
	envs    stringSlice
}

// stringSlice is a flag.Value that accumulates --env NAME=VALUE flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func applyCmd(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var f applyFlags
	fs.StringVar(&f.file, "file", "town.toml", "path to town.toml manifest")
	fs.BoolVar(&f.dryRun, "dry-run", false, "print planned changes without writing to Dolt")
	fs.StringVar(&f.doltDSN, "dolt-dsn", "", "Dolt MySQL DSN (overrides GT_DOLT_DSN env)")
	fs.Var(&f.envs, "env", "set NAME=VALUE in the apply environment (may be repeated)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Apply --env overrides into the process environment before any path expansion.
	for _, kv := range f.envs {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("--env %q: expected NAME=VALUE", kv)
		}
		if err := os.Setenv(parts[0], parts[1]); err != nil {
			return fmt.Errorf("setenv %q: %w", parts[0], err)
		}
	}

	// 1. Read the base manifest file.
	rawBase, err := os.ReadFile(f.file)
	if err != nil {
		return fmt.Errorf("read %s: %w", f.file, err)
	}

	// 2. Parse and validate base manifest.
	m, err := manifest.Parse(rawBase)
	if err != nil {
		return fmt.Errorf("%s: %w", f.file, err)
	}

	// 3. Resolve includes and merge overlay fragments.
	baseDir := filepath.Dir(f.file)
	if err := resolveIncludes(m, baseDir); err != nil {
		return fmt.Errorf("includes: %w", err)
	}

	// 4. Validate apply-time constraints (claude_md paths exist, etc.).
	if err := manifest.ValidateApplyTime(m); err != nil {
		return err
	}

	// 4a. Warn about extension slots (e.g. [[rig.role]] blocks).
	for _, w := range manifest.WarnExtensionSlots(m) {
		slog.Warn("extension slot ignored", "detail", w)
	}

	// 5. Resolve secrets.
	if err := resolveSecrets(m); err != nil {
		return fmt.Errorf("secrets: %w", err)
	}

	// 5c. Merge extends chains: for each [[role]] that declares identity.extends,
	// merge the CLAUDE.md chain and write to ${GT_HOME}/roles/merged/<name>.md.
	// Updates role.Identity.ClaudeMD in-place so SQL generation stores the merged
	// path in desired_custom_roles.
	gtHome := os.ExpandEnv(m.Town.Home)
	if err := townctl.MergeAndWriteExtendsChains(m, gtHome); err != nil {
		return fmt.Errorf("extends merge: %w", err)
	}

	// 6. Determine Dolt DSN.
	dsn := f.doltDSN
	if dsn == "" {
		dsn = os.Getenv("GT_DOLT_DSN")
	}
	if dsn == "" {
		// Construct default DSN from town config.
		port := m.Town.DoltPort
		if port == 0 {
			port = 3306
		}
		dsn = fmt.Sprintf("root@tcp(127.0.0.1:%d)/gastown?parseTime=true", port)
	}

	// 7. Diff and apply.
	if f.dryRun {
		return applyDryRun(m, dsn)
	}
	return applyWrite(m, dsn)
}

// resolveIncludes evaluates each glob pattern in m.Includes, parses each
// matching TOML file as an overlay fragment, and merges its [[rig]] and
// [[role]] entries into m.
//
// Merge semantics (ADR-0001, dgt-cfi):
//   - Rigs: matched by name; if a rig with the same name already exists in m,
//     the include fragment's rig overrides it. New rigs are appended.
//   - Roles: matched by name; same override/append semantics.
//   - Duplicate names within a single include file are an error.
func resolveIncludes(m *manifest.TownManifest, baseDir string) error {
	if len(m.Includes) == 0 {
		return nil
	}

	// Track which paths we have already processed to guard against duplicates
	// caused by overlapping glob patterns.
	seen := make(map[string]struct{})

	for _, pattern := range m.Includes {
		absPattern := pattern
		if !filepath.IsAbs(pattern) {
			absPattern = filepath.Join(baseDir, pattern)
		}
		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
		for _, path := range matches {
			absPath, _ := filepath.Abs(path)
			if _, dup := seen[absPath]; dup {
				continue // already merged from an earlier glob pattern
			}
			seen[absPath] = struct{}{}

			raw, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read include %s: %w", path, err)
			}

			var frag includeFragment
			if err := toml.Unmarshal(raw, &frag); err != nil {
				return fmt.Errorf("parse include %s: %w", path, err)
			}
			if err := mergeFragment(m, &frag, path); err != nil {
				return fmt.Errorf("merge include %s: %w", path, err)
			}
		}
	}
	return nil
}

// includeFragment is a partial town.toml that may contain [[rig]] and/or
// [[role]] entries. Only these two array-of-tables are merged.
type includeFragment struct {
	Rigs  []manifest.RigSpec  `toml:"rig"`
	Roles []manifest.RoleSpec `toml:"role"`
}

func mergeFragment(m *manifest.TownManifest, frag *includeFragment, srcPath string) error {
	// Check for duplicates within the fragment itself.
	fragRigNames := make(map[string]struct{}, len(frag.Rigs))
	for _, r := range frag.Rigs {
		if _, dup := fragRigNames[r.Name]; dup {
			return fmt.Errorf("duplicate rig name %q in %s", r.Name, srcPath)
		}
		fragRigNames[r.Name] = struct{}{}
	}
	fragRoleNames := make(map[string]struct{}, len(frag.Roles))
	for _, r := range frag.Roles {
		if _, dup := fragRoleNames[r.Name]; dup {
			return fmt.Errorf("duplicate role name %q in %s", r.Name, srcPath)
		}
		fragRoleNames[r.Name] = struct{}{}
	}

	// Merge rigs: override by name, append new.
	rigIdx := make(map[string]int, len(m.Rigs))
	for i, r := range m.Rigs {
		rigIdx[r.Name] = i
	}
	for _, r := range frag.Rigs {
		if idx, exists := rigIdx[r.Name]; exists {
			m.Rigs[idx] = r
		} else {
			m.Rigs = append(m.Rigs, r)
		}
	}

	// Merge roles: override by name, append new.
	roleIdx := make(map[string]int, len(m.Roles))
	for i, r := range m.Roles {
		roleIdx[r.Name] = i
	}
	for _, r := range frag.Roles {
		if idx, exists := roleIdx[r.Name]; exists {
			m.Roles[idx] = r
		} else {
			m.Roles = append(m.Roles, r)
		}
	}

	return nil
}

// resolveSecrets expands env-var references in m.Secrets and merges any
// external secrets file. Fast-fails if a required secret is empty.
func resolveSecrets(m *manifest.TownManifest) error {
	// Merge an external secrets file first (values may set env vars used below).
	if m.Secrets.File != "" {
		path := os.ExpandEnv(m.Secrets.File)
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("secrets file %s: %w", path, err)
		}
		var extra struct {
			AnthropicAPIKey string `toml:"anthropic_api_key"`
			GitHubToken     string `toml:"github_token"`
		}
		if err := toml.Unmarshal(raw, &extra); err != nil {
			return fmt.Errorf("parse secrets file %s: %w", path, err)
		}
		if extra.AnthropicAPIKey != "" && m.Secrets.AnthropicAPIKey == "" {
			m.Secrets.AnthropicAPIKey = extra.AnthropicAPIKey
		}
		if extra.GitHubToken != "" && m.Secrets.GitHubToken == "" {
			m.Secrets.GitHubToken = extra.GitHubToken
		}
	}

	// Expand env-var references.
	if ref := m.Secrets.AnthropicAPIKey; ref != "" {
		val := os.ExpandEnv(ref)
		if val == "" {
			return fmt.Errorf("secrets.anthropic_api_key: %q resolved to empty string", ref)
		}
		m.Secrets.AnthropicAPIKey = val
	}
	if ref := m.Secrets.GitHubToken; ref != "" {
		val := os.ExpandEnv(ref)
		if val == "" {
			return fmt.Errorf("secrets.github_token: %q resolved to empty string", ref)
		}
		m.Secrets.GitHubToken = val
	}

	return nil
}

// applyDryRun connects to Dolt (if available), computes the diff, prints it,
// and exits 0. If the Dolt connection fails, it falls back to printing the
// full desired state as an "add" diff.
func applyDryRun(m *manifest.TownManifest, dsn string) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open dolt connection: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	if pingErr := db.PingContext(ctx); pingErr != nil {
		// Dolt not reachable — print full desired state as adds.
		fmt.Println("(dolt not reachable — showing full desired state as additions)")
		printDryRunNoCurrentState(m)
		return nil
	}

	// Read current cost policy rows.
	currentCost, err := readCurrentCostPolicy(ctx, db)
	if err != nil {
		return fmt.Errorf("read desired_cost_policy: %w", err)
	}
	plan := townctl.DryRunPlan(m, currentCost)
	fmt.Print(townctl.FormatDryRun(plan))

	// Read current custom role rows.
	currentRoles, err := readCurrentCustomRoles(ctx, db)
	if err != nil {
		return fmt.Errorf("read desired_custom_roles: %w", err)
	}
	currentRigRoles, err := readCurrentRigCustomRoles(ctx, db)
	if err != nil {
		return fmt.Errorf("read desired_rig_custom_roles: %w", err)
	}
	roleDiff := townctl.DiffCustomRoles(m, currentRoles, currentRigRoles)
	fmt.Print(townctl.FormatCustomRolesDryRun(roleDiff))

	return nil
}

func printDryRunNoCurrentState(m *manifest.TownManifest) {
	for _, rig := range m.Rigs {
		fmt.Printf("+ desired_rigs: name=%s repo=%s branch=%s enabled=%v\n",
			rig.Name, rig.Repo, rig.Branch, rig.Enabled)
	}
	for _, role := range m.Roles {
		fmt.Printf("+ desired_custom_roles: name=%s scope=%s trigger_type=%s\n",
			role.Name, role.Scope, role.Trigger.Type)
	}
	costPlan := townctl.DryRunPlan(m, nil)
	fmt.Print(townctl.FormatDryRun(costPlan))
	roleDiff := townctl.DiffCustomRoles(m, nil, nil)
	fmt.Print(townctl.FormatCustomRolesDryRun(roleDiff))
}

// applyWrite connects to Dolt, checks for no-op, and writes the full atomic
// transaction if there are changes.
func applyWrite(m *manifest.TownManifest, dsn string) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open dolt connection: %w", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping dolt: %w", err)
	}

	// Build the full ordered statement list.
	var allStmts []townctl.Stmt
	allStmts = append(allStmts, townctl.TopologyApplySQL(m)...)
	allStmts = append(allStmts, townctl.ApplySQL(m)...)        // cost policy
	allStmts = append(allStmts, townctl.CustomRolesApplySQL(m)...)

	// Execute all statements inside a single transaction.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range allStmts {
		if _, err := tx.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
			return fmt.Errorf("exec statement: %w\nSQL: %s", err, stmt.Query)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	slog.Info("apply successful", "stmt_count", len(allStmts))
	return launchAgents(m)
}

// launchAgents checks [town.agents] entries and ensures they are running.
// Currently handles the Surveyor process.
func launchAgents(m *manifest.TownManifest) error {
	if !m.Town.Agents.Surveyor {
		return nil
	}
	if err := ensureSurveyor(m); err != nil {
		return fmt.Errorf("surveyor: %w", err)
	}
	return nil
}

// ensureSurveyor checks whether a surveyor process is alive and launches one
// if it is not. The surveyor is launched as a background process.
func ensureSurveyor(m *manifest.TownManifest) error {
	// Check for a running surveyor by looking for a pid file or process name.
	// The pid file lives at ${GT_HOME}/.surveyor.pid.
	home := os.ExpandEnv(m.Town.Home)
	pidFile := filepath.Join(home, ".surveyor.pid")

	if alive := processAlive(pidFile); alive {
		slog.Info("surveyor already running", "pid_file", pidFile)
		return nil
	}

	// Resolve the surveyor binary: use 'gt' on PATH by convention.
	gtBin, err := exec.LookPath("gt")
	if err != nil {
		// gt not found — warn but do not fail; manual launch may be intended.
		slog.Warn("surveyor=true but 'gt' binary not found", "error", err.Error())
		return nil
	}

	claudeMD := os.ExpandEnv(m.Town.Agents.SurveyorClaudeMD)

	cmdArgs := []string{"surveyor", "start", "--town", m.Town.Name}
	if claudeMD != "" {
		cmdArgs = append(cmdArgs, "--claude-md", claudeMD)
	}
	if m.Town.Agents.SurveyorModel != "" {
		cmdArgs = append(cmdArgs, "--model", m.Town.Agents.SurveyorModel)
	}

	cmd := exec.Command(gtBin, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start surveyor: %w", err)
	}
	slog.Info("started surveyor", "pid", cmd.Process.Pid)
	return nil
}

// processAlive returns true if the pid file exists and the process is alive.
func processAlive(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	pidStr := strings.TrimSpace(string(data))
	if pidStr == "" {
		return false
	}
	// Check via /proc/<pid> (Linux). Non-Linux platforms may not have /proc.
	_, err = os.Stat(fmt.Sprintf("/proc/%s", pidStr))
	return err == nil
}

// ── Dolt read helpers ─────────────────────────────────────────────────────────

func readCurrentCostPolicy(ctx context.Context, db *sql.DB) ([]townctl.CostPolicyRow, error) {
	const q = `SELECT rig_name, budget_type, daily_budget, warn_at_pct FROM desired_cost_policy`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []townctl.CostPolicyRow
	for rows.Next() {
		var r townctl.CostPolicyRow
		if err := rows.Scan(&r.RigName, &r.BudgetType, &r.DailyBudget, &r.WarnAtPct); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func readCurrentCustomRoles(ctx context.Context, db *sql.DB) ([]townctl.CustomRoleRow, error) {
	const q = `SELECT name, COALESCE(description,''), scope, lifespan,
		trigger_type, COALESCE(trigger_schedule,''), COALESCE(trigger_event,''),
		claude_md_path, COALESCE(model,''), parent_role,
		COALESCE(reports_to,''), max_instances, COALESCE(extends_role,'')
		FROM desired_custom_roles`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []townctl.CustomRoleRow
	for rows.Next() {
		var r townctl.CustomRoleRow
		if err := rows.Scan(
			&r.Name, &r.Description, &r.Scope, &r.Lifespan,
			&r.TriggerType, &r.TriggerSchedule, &r.TriggerEvent,
			&r.ClaudeMDPath, &r.Model, &r.ParentRole,
			&r.ReportsTo, &r.MaxInstances, &r.ExtendsRole,
		); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func readCurrentRigCustomRoles(ctx context.Context, db *sql.DB) ([]townctl.RigCustomRoleRow, error) {
	const q = `SELECT rig_name, role_name FROM desired_rig_custom_roles WHERE enabled = TRUE`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []townctl.RigCustomRoleRow
	for rows.Next() {
		var r townctl.RigCustomRoleRow
		if err := rows.Scan(&r.RigName, &r.RoleName); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
