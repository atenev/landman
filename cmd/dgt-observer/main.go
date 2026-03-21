// dgt-observer is the dgt observability polling binary. It connects to Dolt,
// reads topology and Beads workflow state on every poll cycle, and exposes the
// results as Prometheus metrics.
//
// Usage:
//
//	dgt-observer [--dolt-dsn DSN] [--interval 15s] [--metrics-addr :9091] [--log-level info]
//
// Flags:
//   - --dolt-dsn: Dolt MySQL DSN (overridden by DGT_DOLT_DSN env var)
//   - --interval: poll interval (default 15s)
//   - --metrics-addr: HTTP listen address for /metrics and /healthz (default :9091)
//   - --log-level: debug | info | warn (default info)
//
// Each metric group (topology, beads) is queried independently. A failure in
// one group increments dgt_dolt_poll_errors_total and logs a warning, but
// does not prevent the other groups from running (ADR-0011 D4).
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	// Register the MySQL driver for Dolt's MySQL-compatible endpoint.
	_ "github.com/go-sql-driver/mysql"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/tenev/dgt/pkg/observer"
	"github.com/tenev/dgt/pkg/surveyor"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("dgt-observer", flag.ContinueOnError)
	doltDSN := flags.String("dolt-dsn", envOrDefault("DGT_DOLT_DSN", ""), "Dolt MySQL DSN (or set DGT_DOLT_DSN)")
	interval := flags.Duration("interval", 15*time.Second, "poll interval")
	metricsAddr := flags.String("metrics-addr", ":9091", "HTTP listen address for /metrics and /healthz")
	logLevel := flags.String("log-level", "info", "log level: debug|info|warn")

	if err := flags.Parse(args); err != nil {
		return 1
	}

	// Configure structured JSON logging with the requested level.
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	default:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if *doltDSN == "" {
		slog.Error("Dolt DSN is required: set --dolt-dsn or DGT_DOLT_DSN")
		return 1
	}

	// Connect to Dolt with exponential backoff, max 5 attempts.
	db, err := connectWithRetry(*doltDSN, 5)
	if err != nil {
		slog.Error("failed to connect to Dolt", "error", err)
		return 1
	}
	defer db.Close()
	slog.Info("connected to Dolt")

	// Register all Prometheus metrics into a private registry (not the global
	// default) so that this binary can be embedded in tests without conflicts.
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	// HTTP server: /metrics (Prometheus) + /healthz (readiness probe).
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	srv := &http.Server{Addr: *metricsAddr, Handler: mux}

	srvErrCh := make(chan error, 1)
	go func() {
		slog.Info("metrics server listening", "addr", *metricsAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			srvErrCh <- err
		}
	}()

	// Graceful shutdown on SIGTERM / SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg := surveyor.DefaultProductionConfig()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	slog.Info("poll loop started", "interval", interval.String(), "metrics_addr", *metricsAddr)

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutdown signal received, draining HTTP server")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				slog.Warn("HTTP server shutdown error", "error", err)
			}
			return 0

		case err := <-srvErrCh:
			slog.Error("metrics server failed", "error", err)
			return 1

		case <-ticker.C:
			poll(ctx, db, m, *interval, cfg)
		}
	}
}

// connectWithRetry opens a *sql.DB and pings Dolt with exponential backoff.
// Delays: 1s, 2s, 4s, 8s before the final attempt (max 5 attempts total).
func connectWithRetry(dsn string, maxAttempts int) (*sql.DB, error) {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if i > 0 {
			delay := time.Duration(math.Pow(2, float64(i-1))) * time.Second
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			slog.Info("retrying Dolt connection", "attempt", i+1, "max", maxAttempts, "delay", delay.String())
			time.Sleep(delay)
		}

		db, err := sql.Open("mysql", dsn)
		if err != nil {
			lastErr = err
			continue
		}
		if err := db.Ping(); err != nil {
			db.Close()
			lastErr = err
			continue
		}
		return db, nil
	}
	return nil, fmt.Errorf("dolt connection failed after %d attempts: %w", maxAttempts, lastErr)
}

// poll runs one complete observation cycle: reads topology + beads from Dolt
// and updates all Prometheus metrics. Each group is independent (ADR-0011 D4).
func poll(ctx context.Context, db *sql.DB, m *observer.Metrics, interval time.Duration, cfg surveyor.VerifyConfig) {
	cycleStart := time.Now()

	// Group 1: topology (desired + actual tables).
	topoSnap, topoErr := observer.ReadTopology(ctx, db)
	if topoErr != nil {
		slog.Warn("topology read error", "error", topoErr)
		m.DoltPollErrorsTotal.Inc()
	}

	// Group 2: beads workflow (bd_issues table).
	// Window is 2× interval per ADR-0011 D3 to avoid gaps at poll boundaries.
	beadsSnap, beadsErr := observer.ReadBeads(ctx, db, 2*interval)
	if beadsErr != nil {
		slog.Warn("beads read error", "error", beadsErr)
		m.DoltPollErrorsTotal.Inc()
	}

	now := cycleStart
	if topoErr == nil {
		updateTopologyMetrics(m, topoSnap, cfg, now)
	}
	if beadsErr == nil {
		updateBeadsMetrics(m, beadsSnap)
	}

	elapsed := time.Since(cycleStart)
	m.DoltPollDurationSeconds.Observe(elapsed.Seconds())
	slog.Debug("poll cycle complete", "duration_ms", elapsed.Milliseconds())
}

// updateTopologyMetrics writes topology-derived gauges.
func updateTopologyMetrics(m *observer.Metrics, snap observer.TopologySnapshot, cfg surveyor.VerifyConfig, now time.Time) {
	// Overall fleet convergence score (aggregate across all rigs).
	result := surveyor.ComputeScore(snap.Desired, snap.Actual, cfg, now)
	m.FleetConvergenceScore.WithLabelValues("__fleet__").Set(result.Score)

	// Agent staleness: seconds since last heartbeat, per (rig, role).
	for _, ag := range snap.Actual.Agents {
		staleness := now.Sub(ag.LastSeen).Seconds()
		if staleness < 0 {
			staleness = 0
		}
		m.AgentStalenessSeconds.WithLabelValues(ag.RigName, ag.Role).Set(staleness)
	}

	// Pool sizes per rig.
	desiredPoolByRig := make(map[string]int, len(snap.Desired.Rigs))
	for _, dr := range snap.Desired.Rigs {
		desiredPoolByRig[dr.Name] = dr.MaxPolecats
	}
	actualPoolByRig := make(map[string]int)
	for _, ag := range snap.Actual.Agents {
		if ag.Role == "polecat" && (ag.Status == "running" || ag.Status == "starting") {
			actualPoolByRig[ag.RigName]++
		}
	}
	for rigName, desired := range desiredPoolByRig {
		actual := actualPoolByRig[rigName]
		m.PoolSizeDesired.WithLabelValues(rigName).Set(float64(desired))
		m.PoolSizeActual.WithLabelValues(rigName).Set(float64(actual))
		m.PoolSizeDelta.WithLabelValues(rigName).Set(float64(desired - actual))
	}

	// Worktrees per (rig, status).
	worktreesByRigStatus := make(map[string]map[string]int)
	for _, wt := range snap.Actual.Worktrees {
		if _, ok := worktreesByRigStatus[wt.RigName]; !ok {
			worktreesByRigStatus[wt.RigName] = make(map[string]int)
		}
		worktreesByRigStatus[wt.RigName][wt.Status]++
	}
	for rigName, statusCounts := range worktreesByRigStatus {
		for status, count := range statusCounts {
			m.WorktreesTotal.WithLabelValues(rigName, status).Set(float64(count))
		}
	}
}

// updateBeadsMetrics writes Beads-derived gauges, counters, and histograms.
func updateBeadsMetrics(m *observer.Metrics, snap observer.BeadsSnapshot) {
	// Open issue counts by (type, priority).
	for key, count := range snap.OpenByTypePriority {
		m.BeadsOpenTotal.WithLabelValues(key.Type, strconv.Itoa(key.Priority)).Set(float64(count))
	}

	// Latency histogram: one observation per recently-closed issue.
	// Also increment BeadsClosedTotal for each observed closed issue.
	for _, sample := range snap.RecentLatencies {
		m.BeadsLatencySeconds.WithLabelValues(sample.Type).Observe(sample.Seconds)
		m.BeadsClosedTotal.Add(1)
	}
}

// envOrDefault returns the value of the environment variable named key, or
// defaultVal if the variable is unset or empty.
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
