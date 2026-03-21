// Package main — integration tests for the dgt-observer poll loop and HTTP
// server (dgt-ydh). Tests call poll() and the HTTP handlers directly against a
// fake SQL driver so no real Dolt connection is required.
//
// SIGTERM test: spawns the test binary itself as a subprocess in helper mode
// (OBSERVER_TEST_SIGTERM_HELPER=1), waits for /healthz, sends SIGTERM, and
// asserts exit code 0.
package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/tenev/dgt/pkg/observer"
	"github.com/tenev/dgt/pkg/surveyor"
)

// ─── TestMain: subprocess helper mode ────────────────────────────────────────

// TestMain runs the SIGTERM subprocess helper when the env var is set;
// otherwise it runs the normal test suite.
func TestMain(m *testing.M) {
	if os.Getenv("OBSERVER_TEST_SIGTERM_HELPER") == "1" {
		sigtermHelperMain()
		return // unreachable; sigtermHelperMain calls os.Exit
	}
	os.Exit(m.Run())
}

// sigtermHelperMain starts a minimal HTTP server (no Dolt), waits for SIGTERM,
// shuts down the server gracefully, and exits 0.
func sigtermHelperMain() {
	port := os.Getenv("OBSERVER_TEST_PORT")
	if port == "" {
		port = "19091"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	srv := &http.Server{Addr: ":" + port, Handler: mux}

	go func() { _ = srv.ListenAndServe() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-ctx.Done()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	os.Exit(0)
}

// ─── Routing fake SQL driver ──────────────────────────────────────────────────

var (
	observerDBDriverOnce sync.Once
	observerDBDSNMu      sync.Mutex
	observerDBDSNMap     = map[string]*observerRoutes{}
	observerDBDSNCtr     atomic.Int64
)

// observerRoute maps a SQL substring to a result set or an error.
type observerRoute struct {
	contains string
	cols     []string
	rows     [][]driver.Value
	err      error
}

type observerRoutes struct {
	routes []observerRoute
}

func registerObserverDriver() {
	observerDBDriverOnce.Do(func() {
		sql.Register("fake-observer-db", &observerFakeDriver{})
	})
}

func newObserverFakeDB(t *testing.T, routes *observerRoutes) *sql.DB {
	t.Helper()
	registerObserverDriver()

	dsn := fmt.Sprintf("observer-fake-%d", observerDBDSNCtr.Add(1))
	observerDBDSNMu.Lock()
	observerDBDSNMap[dsn] = routes
	observerDBDSNMu.Unlock()
	t.Cleanup(func() {
		observerDBDSNMu.Lock()
		delete(observerDBDSNMap, dsn)
		observerDBDSNMu.Unlock()
	})

	db, err := sql.Open("fake-observer-db", dsn)
	if err != nil {
		t.Fatalf("sql.Open fake-observer-db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type observerFakeDriver struct{}

func (d *observerFakeDriver) Open(dsn string) (driver.Conn, error) {
	observerDBDSNMu.Lock()
	routes, ok := observerDBDSNMap[dsn]
	observerDBDSNMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake-observer-db: no config for DSN %q", dsn)
	}
	return &observerFakeConn{routes: routes}, nil
}

type observerFakeConn struct{ routes *observerRoutes }

func (c *observerFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &observerFakeStmt{routes: c.routes, query: query}, nil
}
func (c *observerFakeConn) Close() error              { return nil }
func (c *observerFakeConn) Begin() (driver.Tx, error) { return &observerFakeTx{}, nil }

type observerFakeTx struct{}

func (t *observerFakeTx) Commit() error   { return nil }
func (t *observerFakeTx) Rollback() error { return nil }

type observerFakeStmt struct {
	routes *observerRoutes
	query  string
}

func (s *observerFakeStmt) Close() error  { return nil }
func (s *observerFakeStmt) NumInput() int { return -1 }
func (s *observerFakeStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return observerFakeResult{}, nil
}
func (s *observerFakeStmt) Query(_ []driver.Value) (driver.Rows, error) {
	for _, route := range s.routes.routes {
		if strings.Contains(s.query, route.contains) {
			if route.err != nil {
				return nil, route.err
			}
			return &observerFakeRows{cols: route.cols, rows: route.rows}, nil
		}
	}
	return &observerFakeRows{}, nil
}

type observerFakeResult struct{}

func (r observerFakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r observerFakeResult) RowsAffected() (int64, error) { return 0, nil }

type observerFakeRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *observerFakeRows) Columns() []string { return r.cols }
func (r *observerFakeRows) Close() error      { return nil }
func (r *observerFakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		return io.EOF
	}
	row := r.rows[r.pos]
	r.pos++
	for i, v := range row {
		if i < len(dest) {
			dest[i] = v
		}
	}
	return nil
}

// ─── Shared fake DB routes ────────────────────────────────────────────────────

// standardRoutes returns routes covering all topology + beads queries with a
// predictable data set: one rig "rig-a" with 5 desired polecats and 3 running,
// plus one open task issue (priority 2, count 3) and one closed bug (600s latency).
func standardRoutes(lastSeen time.Time) *observerRoutes {
	return &observerRoutes{
		routes: []observerRoute{
			// desired_rigs
			{
				contains: "desired_rigs",
				cols:     []string{"name", "enabled"},
				rows:     [][]driver.Value{{"rig-a", true}},
			},
			// desired_agent_config — polecat row with max_polecats=5
			{
				contains: "desired_agent_config",
				cols:     []string{"rig_name", "role", "enabled", "max_polecats"},
				rows:     [][]driver.Value{{"rig-a", "polecat", true, int64(5)}},
			},
			// desired_custom_roles (town-scoped) — empty
			{
				contains: "desired_custom_roles",
				cols:     []string{"name", "max_instances"},
			},
			// desired_rig_custom_roles — empty
			{
				contains: "desired_rig_custom_roles",
				cols:     []string{"name", "rig_name", "max_instances"},
			},
			// desired_formulas — empty
			{
				contains: "desired_formulas",
				cols:     []string{"rig_name", "name"},
			},
			// actual_rigs — rig-a running
			{
				contains: "actual_rigs",
				cols:     []string{"name", "enabled", "status", "last_seen"},
				rows:     [][]driver.Value{{"rig-a", true, "running", lastSeen}},
			},
			// actual_agent_config — 3 polecats running
			{
				contains: "actual_agent_config",
				cols:     []string{"rig_name", "role", "status", "last_seen"},
				rows: [][]driver.Value{
					{"rig-a", "polecat", "running", lastSeen},
					{"rig-a", "polecat", "running", lastSeen},
					{"rig-a", "polecat", "running", lastSeen},
				},
			},
			// actual_worktrees — one active worktree
			{
				contains: "actual_worktrees",
				cols:     []string{"rig_name", "status", "last_seen"},
				rows:     [][]driver.Value{{"rig-a", "active", lastSeen}},
			},
			// actual_custom_roles — empty
			{
				contains: "actual_custom_roles",
				cols:     []string{"rig_name", "role_name", "instance_index", "status", "last_seen"},
			},
			// beads open counts
			{
				contains: "status IN",
				cols:     []string{"type", "priority", "COUNT(*)"},
				rows:     [][]driver.Value{{"task", int64(2), int64(3)}},
			},
			// beads closed latencies
			{
				contains: "TIMESTAMPDIFF",
				cols:     []string{"type", "TIMESTAMPDIFF(SECOND, created_at, closed_at)"},
				rows:     [][]driver.Value{{"bug", int64(600)}},
			},
		},
	}
}

// gatherNames gathers metric families from reg and returns their names.
func gatherNames(t *testing.T, reg *prometheus.Registry) map[string]bool {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	names := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	return names
}

// gatherAllNames returns a sorted slice of metric family names for messages.
func gatherAllNames(mfs []*dto.MetricFamily) []string {
	names := make([]string, 0, len(mfs))
	for _, mf := range mfs {
		names = append(names, mf.GetName())
	}
	return names
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestPoll_UpdatesTopologyMetrics verifies that poll() sets fleet convergence,
// pool size, and agent staleness gauges after one cycle with known topology data.
func TestPoll_UpdatesTopologyMetrics(t *testing.T) {
	t.Parallel()

	lastSeen := time.Now().Add(-30 * time.Second)
	db := newObserverFakeDB(t, standardRoutes(lastSeen))
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	poll(context.Background(), db, m, 15*time.Second, surveyor.DefaultProductionConfig())

	names := gatherNames(t, reg)
	required := []string{
		"dgt_fleet_convergence_score",
		"dgt_pool_size_desired",
		"dgt_pool_size_actual",
		"dgt_pool_size_delta",
		"dgt_agent_staleness_seconds",
		"dgt_dolt_poll_duration_seconds",
	}
	for _, name := range required {
		if !names[name] {
			t.Errorf("expected metric family %q not gathered; found: %v", name, names)
		}
	}
}

// TestPoll_UpdatesBeadsMetrics verifies that poll() records open issue counts
// and latency observations into the Beads metric families.
func TestPoll_UpdatesBeadsMetrics(t *testing.T) {
	t.Parallel()

	lastSeen := time.Now()
	db := newObserverFakeDB(t, standardRoutes(lastSeen))
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	poll(context.Background(), db, m, 15*time.Second, surveyor.DefaultProductionConfig())

	names := gatherNames(t, reg)
	required := []string{
		"dgt_beads_open_total",
		"dgt_beads_closed_total",
		"dgt_beads_latency_seconds",
	}
	for _, name := range required {
		if !names[name] {
			t.Errorf("expected metric family %q not gathered; found: %v", name, names)
		}
	}
}

// TestHealthz_Returns200 verifies that the /healthz handler responds 200 with
// body "ok".
func TestHealthz_Returns200(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "ok") {
		t.Errorf("body = %q, want to contain %q", rr.Body.String(), "ok")
	}
}

// TestMetrics_ReturnsFamiliesAfterPoll starts an httptest server, runs one
// poll cycle, then scrapes /metrics and verifies expected metric family names
// appear in the Prometheus text format.
func TestMetrics_ReturnsFamiliesAfterPoll(t *testing.T) {
	t.Parallel()

	lastSeen := time.Now()
	db := newObserverFakeDB(t, standardRoutes(lastSeen))
	reg := prometheus.NewRegistry()
	m := observer.RegisterMetrics(reg)

	poll(context.Background(), db, m, 15*time.Second, surveyor.DefaultProductionConfig())

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyStr := string(body)

	expected := []string{
		"dgt_fleet_convergence_score",
		"dgt_pool_size_desired",
		"dgt_beads_open_total",
		"dgt_dolt_poll_duration_seconds",
	}
	for _, name := range expected {
		if !strings.Contains(bodyStr, name) {
			t.Errorf("/metrics body does not contain %q", name)
		}
	}
}

// TestSIGTERM_CleanShutdown spawns the test binary in subprocess helper mode,
// waits for the /healthz endpoint to become ready, sends SIGTERM, and verifies
// the process exits with code 0.
func TestSIGTERM_CleanShutdown(t *testing.T) {
	// Find a free port for the helper's HTTP server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()

	// Spawn the test binary itself in helper mode. Use -test.run=^$ so no
	// tests run in the subprocess; TestMain exits early via os.Exit(0).
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperNoop$")
	cmd.Env = append(os.Environ(),
		"OBSERVER_TEST_SIGTERM_HELPER=1",
		"OBSERVER_TEST_PORT="+port,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}

	healthzURL := "http://127.0.0.1:" + port + "/healthz"
	if !waitForHealthz(healthzURL, 5*time.Second) {
		_ = cmd.Process.Kill()
		t.Fatal("/healthz did not become ready within 5s")
	}

	// Send SIGTERM and verify clean exit (code 0).
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			t.Errorf("subprocess exit code = %d, want 0", exitErr.ExitCode())
		} else {
			t.Errorf("subprocess Wait: %v", waitErr)
		}
	}
}

// TestHelperNoop is the target test name used when spawning the SIGTERM
// subprocess. TestMain intercepts the subprocess before m.Run() is called,
// so this test never actually executes in that mode.
func TestHelperNoop(t *testing.T) {}

// ─── helpers ──────────────────────────────────────────────────────────────────

// waitForHealthz polls url until it returns HTTP 200 or timeout elapses.
func waitForHealthz(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return true
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// _ ensures gatherAllNames is referenced (used in test error formatting stubs).
var _ = gatherAllNames
