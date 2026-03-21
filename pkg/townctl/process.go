// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file implements process liveness checks and initial launch for
// [town.agents] entries (ADR-0002 Decision 6; docs/townctl/design.md Step 10).
//
// town-ctl only performs the initial launch of declared agents. Ongoing
// supervision is the responsibility of systemd (or Deacon Formulas in future).
package townctl

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// SurveyorTuning holds the manifest-level tuning parameters forwarded to the
// Surveyor process as environment variables when it is launched by EnsureSurveyor.
// Zero values mean "use the Surveyor's compiled-in default" and are not forwarded.
type SurveyorTuning struct {
	// ConvergenceThreshold overrides GT_SURVEYOR_CONVERGENCE_THRESHOLD.
	// Must be in (0.0, 1.0] when non-zero. Corresponds to
	// manifest.TownAgents.SurveyorConvergenceThreshold.
	ConvergenceThreshold float64
	// MaxRetries overrides GT_SURVEYOR_MAX_RETRIES.
	// Must be >= 1 when non-zero. Corresponds to
	// manifest.TownAgents.SurveyorRetryCount.
	MaxRetries int
	// PatrolIntervalSeconds overrides GT_DEACON_PATROL_INTERVAL_SECONDS passed
	// to the Surveyor so it can forward the value when configuring Deacon.
	// Must be >= 10 when non-zero. Corresponds to
	// manifest.TownCostConfig.PatrolIntervalSeconds.
	PatrolIntervalSeconds int
}

// EnsureSurveyor checks whether the Surveyor process is running. If not, it
// launches `surveyor --config <configDir>` as a detached process.
//
// PID file location: <gtHome>/run/surveyor.pid. If the file exists and the
// PID refers to a live process (kill -0 succeeds), Surveyor is already running.
// If the PID file is missing or the process is dead, Surveyor is launched.
//
// configDir is the directory containing town.toml; it is passed to the Surveyor
// as its --config argument so it can locate its configuration at startup.
//
// Non-zero fields in tuning are forwarded as environment variables so the
// Surveyor uses the manifest-declared values instead of its compiled-in defaults:
//
//	GT_SURVEYOR_CONVERGENCE_THRESHOLD  — ConvergenceThreshold (float, e.g. "0.95")
//	GT_SURVEYOR_MAX_RETRIES            — MaxRetries (integer, e.g. "5")
//	GT_DEACON_PATROL_INTERVAL_SECONDS  — PatrolIntervalSeconds (integer, e.g. "60")
func EnsureSurveyor(gtHome, configDir string, tuning SurveyorTuning) error {
	pidFile := filepath.Join(gtHome, "run", "surveyor.pid")

	// Check existing PID file.
	if data, err := os.ReadFile(pidFile); err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && pid > 0 {
			process, findErr := os.FindProcess(pid)
			if findErr == nil {
				if signalErr := process.Signal(syscall.Signal(0)); signalErr == nil {
					// Process is alive — Surveyor is already running.
					return nil
				}
			}
		}
	}

	// Launch Surveyor as a detached process.
	cmd := exec.Command("surveyor", "--config", configDir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	// Build env slice with any non-zero tuning overrides appended. Only set
	// cmd.Env when there are extra vars to add; otherwise cmd inherits
	// os.Environ() automatically (cmd.Env == nil means inherit).
	var extraEnv []string
	if tuning.ConvergenceThreshold != 0 {
		extraEnv = append(extraEnv,
			fmt.Sprintf("GT_SURVEYOR_CONVERGENCE_THRESHOLD=%g", tuning.ConvergenceThreshold))
	}
	if tuning.MaxRetries != 0 {
		extraEnv = append(extraEnv,
			fmt.Sprintf("GT_SURVEYOR_MAX_RETRIES=%d", tuning.MaxRetries))
	}
	if tuning.PatrolIntervalSeconds != 0 {
		extraEnv = append(extraEnv,
			fmt.Sprintf("GT_DEACON_PATROL_INTERVAL_SECONDS=%d", tuning.PatrolIntervalSeconds))
	}
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("surveyor: launch: %w", err)
	}

	// Write PID file so subsequent runs can detect the running instance.
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		return fmt.Errorf("surveyor: mkdir %s: %w", filepath.Dir(pidFile), err)
	}
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return fmt.Errorf("surveyor: write pid file: %w", err)
	}

	// Detach — we do not wait for the child.
	_ = cmd.Process.Release()
	return nil
}
