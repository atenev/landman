// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements the top-level apply pipeline (docs/townctl/design.md).
// It orchestrates all ten steps: parse → resolve includes → env overlay →
// resolve secrets → connect Dolt → diff → dry-run or write → launch agents.
package townctl

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/tenev/dgt/pkg/manifest"
)

// ApplyOptions configures a single town-ctl apply run.
type ApplyOptions struct {
	// DryRun prints the planned changes without writing to Dolt.
	DryRun bool
	// Output is the writer for dry-run plan output. Defaults to os.Stdout when nil.
	Output io.Writer
	// Env, when non-empty, loads town.<Env>.toml from the manifest directory
	// as an overlay (applied last, overrides all other values).
	Env string

	// Logger is the structured logger for diagnostic output. When nil,
	// slog.Default() is used.
	Logger *slog.Logger

	// DoltDSN is a raw MySQL DSN for the Dolt connection (e.g.
	// "root@tcp(127.0.0.1:3306)/gastown?parseTime=true"). When non-empty it
	// takes precedence over the component-based fields below.
	DoltDSN string

	// Dolt connection parameters. Ignored when DoltDSN is non-empty.
	// Defaults are applied from environment variables when zero values.
	DoltHost     string
	DoltPort     int
	DoltDB       string
	DoltUser     string
	DoltPassword string
}

// logger returns the configured logger or the slog default.
func (o *ApplyOptions) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.Default()
}

// applyDefaults fills zero-value Dolt connection fields with defaults.
// Port is left at 0 when DoltDSN is set or when the caller wants the manifest's
// dolt_port to take effect; Apply() applies the final port fallback.
func (o *ApplyOptions) applyDefaults() {
	if o.DoltDSN != "" {
		// DSN is provided directly — skip component defaults.
		return
	}
	if o.DoltHost == "" {
		o.DoltHost = envOrDefault("TOWN_CTL_DOLT_HOST", "localhost")
	}
	// Port default (3306) is applied in Apply() after the manifest is parsed
	// so that m.Town.DoltPort can be used as a higher-priority fallback.
	if o.DoltDB == "" {
		o.DoltDB = envOrDefault("TOWN_CTL_DOLT_DB", "gastown")
	}
	if o.DoltUser == "" {
		o.DoltUser = envOrDefault("TOWN_CTL_DOLT_USER", "root")
	}
	if o.DoltPassword == "" {
		o.DoltPassword = os.Getenv("TOWN_CTL_DOLT_PASSWORD")
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Apply runs the full town-ctl apply pipeline for the manifest at path.
// It writes to stderr on all errors and returns a non-nil error on any failure.
// On --dry-run, no Dolt writes occur; the plan is printed to stdout.
func Apply(manifestPath string, opts ApplyOptions) (retErr error) {
	start := time.Now()
	defer func() {
		outcome := "ok"
		if retErr != nil {
			outcome = "error"
		}
		applyDurationSeconds.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	}()

	opts.applyDefaults()

	// Step 1 — Read and parse the manifest.
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		applyErrorsTotal.WithLabelValues("parse").Inc()
		return fmt.Errorf("%s: read: %w", manifestPath, err)
	}
	m, err := manifest.Parse(data)
	if err != nil {
		applyErrorsTotal.WithLabelValues("parse").Inc()
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	// Step 2 — Validate manifest version.
	if m.Version != "1" {
		applyErrorsTotal.WithLabelValues("parse").Inc()
		return fmt.Errorf("unsupported manifest version %q — upgrade town-ctl to ≥ 0.2.0", m.Version)
	}

	manifestDir := filepath.Dir(manifestPath)

	// Step 3 — Resolve includes.
	if len(m.Includes) > 0 {
		included, err := ResolveIncludes(manifestDir, m.Includes)
		if err != nil {
			applyErrorsTotal.WithLabelValues("includes").Inc()
			return fmt.Errorf("includes: %w", err)
		}
		if err := MergeIncludes(m, included); err != nil {
			applyErrorsTotal.WithLabelValues("includes").Inc()
			return fmt.Errorf("merge includes: %w", err)
		}
	}

	// Step 4 — Apply --env overlay.
	if opts.Env != "" {
		if err := ApplyEnvOverlay(m, manifestDir, opts.Env); err != nil {
			applyErrorsTotal.WithLabelValues("env_overlay").Inc()
			return fmt.Errorf("env overlay: %w", err)
		}
	}

	// Step 5 — Resolve secrets (env-var interpolation).
	if err := ResolveSecrets(m); err != nil {
		applyErrorsTotal.WithLabelValues("secrets").Inc()
		return err
	}

	// Step 5a — Verify that required secrets are non-empty after resolution.
	if err := VerifyRequiredSecrets(m); err != nil {
		return err
	}

	// Step 5b — Apply-time filesystem checks (CLAUDE.md path existence).
	if err := manifest.ValidateApplyTime(m); err != nil {
		applyErrorsTotal.WithLabelValues("validate").Inc()
		return fmt.Errorf("%s: apply-time validation: %w", manifestPath, err)
	}

	// Step 5c — Merge extends chains (ADR-0005): for each [[role]] that declares
	// identity.extends, merge the CLAUDE.md chain and write to
	// ${GT_HOME}/roles/merged/<name>.md. Updates role.Identity.ClaudeMD in-place
	// so that SQL generation stores the merged path in desired_custom_roles.
	gtHome := os.ExpandEnv(m.Town.Home)
	if err := MergeAndWriteExtendsChains(m, gtHome); err != nil {
		applyErrorsTotal.WithLabelValues("extends_merge").Inc()
		return fmt.Errorf("%s: extends merge: %w", manifestPath, err)
	}

	// Emit warnings for unrecognised extension slots.
	log := opts.logger()
	for _, warn := range manifest.WarnExtensionSlots(m) {
		log.Warn("extension slot ignored", "detail", warn)
	}

	// Step 6 — (Dry-run: skip Dolt connection)
	if opts.DryRun {
		out := opts.Output
		if out == nil {
			out = os.Stdout
		}
		return printDryRun(out, m, manifestPath)
	}

	// Step 6 — Connect to Dolt. Bound the initial ping to 10 s to prevent
	// indefinite hangs on misconfigured or slow Dolt instances.
	// When the caller provides a raw DSN, use it directly; otherwise use
	// component-based connection, preferring m.Town.DoltPort over the 3306 default.
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer connectCancel()
	var db *DB
	if opts.DoltDSN != "" {
		db, err = ConnectDSN(connectCtx, opts.DoltDSN)
	} else {
		port := opts.DoltPort
		if port == 0 {
			port = m.Town.DoltPort
		}
		if port == 0 {
			port = 3306
		}
		db, err = Connect(connectCtx, opts.DoltHost, port, opts.DoltDB, opts.DoltUser, opts.DoltPassword)
	}
	if err != nil {
		applyErrorsTotal.WithLabelValues("connect").Inc()
		return err
	}
	defer db.Close()

	// Steps 7–9 — Diff and write atomic transaction.
	if err := applyTransaction(db, m, manifestPath); err != nil {
		applyErrorsTotal.WithLabelValues("transaction").Inc()
		return err
	}

	// Step 10 — Ensure agent processes are running.
	if m.Town.Agents.Surveyor {
		gtHome := m.Town.Home
		tuning := SurveyorTuning{
			ConvergenceThreshold:  m.Town.Agents.SurveyorConvergenceThreshold,
			MaxRetries:            m.Town.Agents.SurveyorRetryCount,
			PatrolIntervalSeconds: m.Town.Cost.PatrolIntervalSeconds,
		}
		surveyorEnv := BuildSurveyorEnv(m.Secrets.AnthropicAPIKey, m.Secrets.GitHubToken)
		if err := EnsureSurveyor(gtHome, manifestDir, tuning, surveyorEnv); err != nil {
			// Non-fatal: log a warning but do not fail the apply. The Surveyor
			// can be started manually or via systemd if auto-launch fails.
			log.Warn("surveyor launch failed", "error", err.Error())
		}
	}

	log.Info("apply complete", "manifest", manifestPath)
	return nil
}

// applyTransaction builds and executes the full atomic Dolt transaction for m.
func applyTransaction(db *DB, m *manifest.TownManifest, manifestPath string) error {
	// Pre-flight: ensure no K8s operator write is in progress (dgt-lc3).
	if err := CheckTopologyLock(db, BinaryVersion); err != nil {
		return fmt.Errorf("apply: %w", err)
	}

	// Build the ordered statement list across all topology tables.
	stmts := FullApplySQL(m)

	// Record per-table, per-action diff op counts before executing.
	recordDiffOps(stmts)

	// Compute human-readable change count for the commit message.
	addUpdateRemove := fmt.Sprintf("[%d stmts]", len(stmts))

	// Set Dolt commit message before COMMIT.
	commitMsg := fmt.Sprintf("town-ctl apply: %s v%s %s",
		manifestPath, m.Version, addUpdateRemove)

	// Prepend SET @dolt_transaction_commit_message and append lock upsert.
	msgStmt := Stmt{
		Query: "SET @dolt_transaction_commit_message = ?;",
		Args:  []any{commitMsg},
	}
	allStmts := append([]Stmt{msgStmt}, stmts...)
	allStmts = append(allStmts, TopologyLockUpsertSQL(BinaryVersion))

	return db.ExecTransaction(allStmts)
}

// recordDiffOps increments topologyDiffOpsTotal for each statement in stmts.
// It extracts the action ("insert", "update", "delete") and table name from
// the first tokens of each SQL query using simple string parsing. Statements
// that do not match a known DML pattern (e.g. SET, system upserts) are skipped.
func recordDiffOps(stmts []Stmt) {
	for _, s := range stmts {
		action, table := parseSQLActionTable(s.Query)
		if action == "" {
			continue
		}
		topologyDiffOpsTotal.WithLabelValues(table, action).Inc()
	}
}

// parseSQLActionTable extracts the DML action keyword and table name from a
// SQL query string. It handles INSERT INTO, DELETE FROM, and UPDATE patterns.
// Returns ("", "") when the query does not match a recognised DML pattern.
func parseSQLActionTable(query string) (action, table string) {
	// Normalise whitespace: collapse multiple spaces/newlines to single space.
	norm := sqlNormaliseSpaces(query)
	if len(norm) < 7 {
		return "", ""
	}
	upper := sqlToUpper(norm)

	switch {
	case len(upper) > 12 && upper[:12] == "INSERT INTO ":
		table = sqlFirstToken(norm[12:])
		return "insert", table
	case len(upper) > 12 && upper[:12] == "DELETE FROM ":
		table = sqlFirstToken(norm[12:])
		return "delete", table
	case len(upper) > 7 && upper[:7] == "UPDATE ":
		table = sqlFirstToken(norm[7:])
		return "update", table
	}
	return "", ""
}

// sqlNormaliseSpaces replaces newlines and tabs with spaces and collapses
// consecutive spaces to a single space, trimming leading/trailing whitespace.
func sqlNormaliseSpaces(s string) string {
	out := make([]byte, 0, len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\t' || c == '\r' {
			c = ' '
		}
		if c == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, c)
	}
	// Trim leading space.
	if len(out) > 0 && out[0] == ' ' {
		out = out[1:]
	}
	return string(out)
}

// sqlToUpper returns an upper-cased copy of s (ASCII only; sufficient for SQL keywords).
func sqlToUpper(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		b[i] = c
	}
	return string(b)
}

// sqlFirstToken returns the first whitespace-delimited token from s.
func sqlFirstToken(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '(' || s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// printDryRun computes and prints the planned topology changes to w.
// No Dolt connection is made. Exit code is 0 (success) even when changes exist.
func printDryRun(w io.Writer, m *manifest.TownManifest, manifestPath string) error {
	fmt.Fprintf(w, "town-ctl dry-run: %s\n", manifestPath)
	fmt.Fprintf(w, "rigs: %d  roles: %d\n\n", len(m.Rigs), len(m.Roles))

	// For --dry-run without a Dolt connection we treat every desired resource
	// as an "add" (no current state to diff against).
	var topoOps []TopologyOp
	for _, rig := range m.Rigs {
		topoOps = append(topoOps, TopologyOp{
			Action: "add",
			Table:  "desired_rigs",
			Key:    fmt.Sprintf("name=%s repo=%s branch=%s enabled=%t", rig.Name, rig.Repo, rig.Branch, rig.Enabled),
		})
	}
	fmt.Fprint(w, FormatTopologyDryRun(topoOps))

	// Custom roles dry-run uses the structured diff formatter.
	customDiff := DiffCustomRoles(m, nil, nil)
	fmt.Fprint(w, FormatCustomRolesDryRun(customDiff))

	costOps := DryRunPlan(m, nil)
	fmt.Fprint(w, FormatDryRun(costOps))
	return nil
}
