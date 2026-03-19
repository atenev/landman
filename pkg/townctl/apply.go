// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements the top-level apply pipeline (docs/townctl/design.md).
// It orchestrates all ten steps: parse → resolve includes → env overlay →
// resolve secrets → connect Dolt → diff → dry-run or write → launch agents.
package townctl

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tenev/dgt/pkg/manifest"
)

// ApplyOptions configures a single town-ctl apply run.
type ApplyOptions struct {
	// DryRun prints the planned changes to stdout without writing to Dolt.
	DryRun bool
	// Env, when non-empty, loads town.<Env>.toml from the manifest directory
	// as an overlay (applied last, overrides all other values).
	Env string

	// Dolt connection parameters. Defaults are applied when zero values.
	DoltHost     string
	DoltPort     int
	DoltDB       string
	DoltUser     string
	DoltPassword string
}

// applyDefaults fills zero-value Dolt connection fields with defaults.
func (o *ApplyOptions) applyDefaults() {
	if o.DoltHost == "" {
		o.DoltHost = envOrDefault("TOWN_CTL_DOLT_HOST", "localhost")
	}
	if o.DoltPort == 0 {
		o.DoltPort = 3306
	}
	if o.DoltDB == "" {
		o.DoltDB = envOrDefault("TOWN_CTL_DOLT_DB", "gas_town")
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
func Apply(manifestPath string, opts ApplyOptions) error {
	opts.applyDefaults()

	// Step 1 — Read and parse the manifest.
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("%s: read: %w", manifestPath, err)
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	// Step 2 — Validate manifest version.
	if m.Version != "1" {
		return fmt.Errorf("unsupported manifest version %q — upgrade town-ctl to ≥ 0.2.0", m.Version)
	}

	manifestDir := filepath.Dir(manifestPath)

	// Step 3 — Resolve includes.
	if len(m.Includes) > 0 {
		included, err := ResolveIncludes(manifestDir, m.Includes)
		if err != nil {
			return err
		}
		if err := MergeIncludes(m, included); err != nil {
			return err
		}
	}

	// Step 4 — Apply --env overlay.
	if opts.Env != "" {
		if err := ApplyEnvOverlay(m, manifestDir, opts.Env); err != nil {
			return err
		}
	}

	// Step 5 — Resolve secrets (env-var interpolation).
	if err := ResolveSecrets(m); err != nil {
		return err
	}

	// Step 5b — Apply-time filesystem checks (CLAUDE.md path existence).
	if err := manifest.ValidateApplyTime(m); err != nil {
		return fmt.Errorf("%s: apply-time validation: %w", manifestPath, err)
	}

	// Emit warnings for unrecognised extension slots.
	for _, warn := range manifest.WarnExtensionSlots(m) {
		fmt.Fprintf(os.Stderr, "town-ctl: WARNING: %s\n", warn)
	}

	// Step 6 — (Dry-run: skip Dolt connection)
	if opts.DryRun {
		return printDryRun(m, manifestPath)
	}

	// Step 6 — Connect to Dolt.
	db, err := Connect(opts.DoltHost, opts.DoltPort, opts.DoltDB, opts.DoltUser, opts.DoltPassword)
	if err != nil {
		return err
	}
	defer db.Close()

	// Steps 7–9 — Diff and write atomic transaction.
	if err := applyTransaction(db, m, manifestPath); err != nil {
		return err
	}

	// Step 10 — Ensure agent processes are running.
	if m.Town.Agents.Surveyor {
		gtHome := m.Town.Home
		if err := EnsureSurveyor(gtHome, manifestDir); err != nil {
			// Non-fatal: log a warning but do not fail the apply. The Surveyor
			// can be started manually or via systemd if auto-launch fails.
			fmt.Fprintf(os.Stderr, "town-ctl: WARNING: %s\n", err)
		}
	}

	fmt.Fprintf(os.Stderr, "town-ctl: apply complete\n")
	return nil
}

// applyTransaction builds and executes the full atomic Dolt transaction for m.
func applyTransaction(db *DB, m *manifest.TownManifest, manifestPath string) error {
	// Build the ordered statement list across all topology tables.
	stmts := FullApplySQL(m)

	// Compute human-readable change count for the commit message.
	addUpdateRemove := fmt.Sprintf("[%d stmts]", len(stmts))

	// Set Dolt commit message before COMMIT.
	commitMsg := fmt.Sprintf("town-ctl apply: %s v%s %s",
		manifestPath, m.Version, addUpdateRemove)

	// Prepend SET @dolt_transaction_commit_message to stmts.
	msgStmt := fmt.Sprintf(
		"SET @dolt_transaction_commit_message = '%s';",
		escapeSQLString(commitMsg))
	allStmts := append([]string{msgStmt}, stmts...)

	return db.ExecTransaction(allStmts)
}

// printDryRun computes and prints the planned topology changes to stdout.
// No Dolt connection is made. Exit code is 0 (success) even when changes exist.
func printDryRun(m *manifest.TownManifest, manifestPath string) error {
	fmt.Printf("town-ctl dry-run: %s\n", manifestPath)
	fmt.Printf("rigs: %d  roles: %d\n\n", len(m.Rigs), len(m.Roles))

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
	fmt.Print(FormatTopologyDryRun(topoOps))

	// Custom roles dry-run uses the structured diff formatter.
	customDiff := DiffCustomRoles(m, nil, nil)
	fmt.Print(FormatCustomRolesDryRun(customDiff))

	costOps := DryRunPlan(m, nil)
	fmt.Print(FormatDryRun(costOps))
	return nil
}
