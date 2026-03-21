// town-ctl is the Gas Town topology actuator. It reads a town.toml manifest
// and writes the desired topology to a Dolt instance.
//
// Usage:
//
//	town-ctl apply [--file town.toml] [--dry-run] [--env NAME=VALUE...]
//	               [--dolt-dsn DSN]
//	town-ctl status [--dolt-dsn DSN] [--output text|json] [--rig NAME]
//	                [--no-color]
//	town-ctl lock clear [--dolt-dsn DSN]
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
// The lock command:
//
//	lock clear — force-expire the desired_topology_lock advisory lock.
//	             Use when a writer crashed mid-write and the 30 s TTL
//	             has not yet elapsed. See operator runbook for caveats.
//
// Exit codes: 0 = success, 1 = error (human-readable message on stderr).
// For status: 0 = fully converged, 1 = error, 2 = not fully converged.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	// Register the MySQL driver used for Dolt's MySQL-compatible endpoint.
	_ "github.com/go-sql-driver/mysql"

	"github.com/tenev/dgt/pkg/townctl"
)

func main() {
	// Configure structured logging to stderr for all diagnostic output.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		slog.Error("missing command", "usage", "town-ctl <apply|status|version> [flags]")
		return 1
	}
	switch args[0] {
	case "apply":
		if err := applyCmd(args[1:]); err != nil {
			slog.Error("apply failed", "error", err.Error())
			return 1
		}
		return 0
	case "status":
		return statusCmd(args[1:])
	case "lock":
		return lockCmd(args[1:])
	case "version":
		fmt.Printf("%s\n", townctl.BinaryVersion)
		return 0
	default:
		slog.Error("unknown command", "command", args[0], "usage", "town-ctl <apply|status|lock|version> [flags]")
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

	// Apply --env overrides into the process environment before any path
	// expansion so that ${VAR} references in the manifest are resolved.
	for _, kv := range f.envs {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("--env %q: expected NAME=VALUE", kv)
		}
		if err := os.Setenv(parts[0], parts[1]); err != nil {
			return fmt.Errorf("setenv %q: %w", parts[0], err)
		}
	}

	// Determine the Dolt DSN: flag → env → empty (Apply uses component defaults).
	dsn := f.doltDSN
	if dsn == "" {
		dsn = os.Getenv("GT_DOLT_DSN")
	}

	return townctl.Apply(f.file, townctl.ApplyOptions{
		DryRun:  f.dryRun,
		DoltDSN: dsn,
	})
}

type statusFlags struct {
	doltDSN string
	output  string
	rigs    stringSlice
	noColor bool
}

// statusCmd implements the town-ctl status subcommand.
// Exit codes: 0 = fully converged, 1 = error, 2 = not fully converged.
func statusCmd(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var f statusFlags
	fs.StringVar(&f.doltDSN, "dolt-dsn", "", "Dolt MySQL DSN (env: GT_DOLT_DSN)")
	fs.StringVar(&f.output, "output", "text", "output format: text or json")
	fs.Var(&f.rigs, "rig", "filter to a single rig name (may be repeated)")
	fs.BoolVar(&f.noColor, "no-color", false, "disable ANSI colour codes")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	dsn := f.doltDSN
	if dsn == "" {
		dsn = os.Getenv("GT_DOLT_DSN")
	}
	if dsn == "" {
		slog.Error("status: Dolt DSN is required; set GT_DOLT_DSN or pass --dolt-dsn")
		return 1
	}

	opts := townctl.StatusOptions{
		DoltDSN: dsn,
		Output:  f.output,
		Rigs:    []string(f.rigs),
		NoColor: f.noColor,
	}

	result, err := townctl.Status(dsn, opts)
	if err != nil {
		slog.Error("status failed", "error", err.Error())
		return 1
	}

	switch f.output {
	case "json":
		b, err := townctl.FormatStatusJSON(result)
		if err != nil {
			slog.Error("status: marshal json", "error", err.Error())
			return 1
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, b, "", "  "); err != nil {
			fmt.Fprintf(os.Stdout, "%s\n", b)
		} else {
			fmt.Fprintf(os.Stdout, "%s\n", indented.String())
		}
	default:
		fmtOpts := townctl.FormatOpts{NoColor: f.noColor}
		fmt.Print(townctl.FormatStatusText(result, fmtOpts))
	}

	// Exit 2 when any rig is not fully converged.
	for _, rig := range result.Rigs {
		if rig.Score < 1.0 {
			return 2
		}
	}
	return 0
}

// lockCmd implements the town-ctl lock subcommand.
//
// Subcommands:
//
//	clear   Force-expire the desired_topology_lock advisory lock.
func lockCmd(args []string) int {
	if len(args) == 0 {
		slog.Error("lock: missing subcommand", "usage", "town-ctl lock <clear> [flags]")
		return 1
	}
	switch args[0] {
	case "clear":
		return lockClearCmd(args[1:])
	default:
		slog.Error("lock: unknown subcommand", "subcommand", args[0], "usage", "town-ctl lock <clear> [flags]")
		return 1
	}
}

// lockClearCmd force-expires the desired_topology_lock advisory lock.
func lockClearCmd(args []string) int {
	fs := flag.NewFlagSet("lock clear", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var doltDSN string
	fs.StringVar(&doltDSN, "dolt-dsn", "", "Dolt MySQL DSN (env: GT_DOLT_DSN)")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	dsn := doltDSN
	if dsn == "" {
		dsn = os.Getenv("GT_DOLT_DSN")
	}
	if dsn == "" {
		slog.Error("lock clear: Dolt DSN is required; set GT_DOLT_DSN or pass --dolt-dsn")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := townctl.ConnectDSN(ctx, dsn)
	if err != nil {
		slog.Error("lock clear: connect to Dolt", "error", err.Error())
		return 1
	}
	defer db.Close()

	if err := townctl.ClearTopologyLock(db); err != nil {
		slog.Error("lock clear: failed", "error", err.Error())
		return 1
	}

	fmt.Fprintln(os.Stdout, "desired_topology_lock cleared")
	return 0
}
