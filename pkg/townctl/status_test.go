// Package townctl — unit tests for statusFromDB, FormatStatusText,
// FormatStatusJSON, and StatusExitCode (dgt-68y).
//
// Uses a whitebox (package townctl) import so that the unexported statusFromDB
// helper is accessible. A routing fake SQL driver dispatches each query to
// per-test row sets without a real Dolt connection.
package townctl

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── routing fake SQL driver ──────────────────────────────────────────────────
//
// Registered once as "fake-status-db". Distinct from the "fakesql" driver in
// dolt_test.go (which is in package townctl_test).

var (
	statusDBDriverOnce sync.Once
	statusDBDSNMu      sync.Mutex
	statusDBDSNMap     = map[string]*statusDBRoutes{}
	statusDBDSNCtr     atomic.Int64
)

// statusDBRoute maps a SQL substring to a set of result rows or an error.
type statusDBRoute struct {
	Contains string
	Cols     []string
	Rows     [][]driver.Value
	Err      error
}

type statusDBRoutes struct {
	routes []statusDBRoute
}

func registerStatusDBDriver() {
	statusDBDriverOnce.Do(func() {
		sql.Register("fake-status-db", &statusDBDriver{})
	})
}

// newStatusDBFake creates a *sql.DB backed by the routing fake.
func newStatusDBFake(t *testing.T, routes *statusDBRoutes) *sql.DB {
	t.Helper()
	registerStatusDBDriver()

	dsn := fmt.Sprintf("statusdb-fake-%d", statusDBDSNCtr.Add(1))
	statusDBDSNMu.Lock()
	statusDBDSNMap[dsn] = routes
	statusDBDSNMu.Unlock()
	t.Cleanup(func() {
		statusDBDSNMu.Lock()
		delete(statusDBDSNMap, dsn)
		statusDBDSNMu.Unlock()
	})

	db, err := sql.Open("fake-status-db", dsn)
	if err != nil {
		t.Fatalf("sql.Open fake-status-db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// ─── driver.Driver ────────────────────────────────────────────────────────────

type statusDBDriver struct{}

func (d *statusDBDriver) Open(dsn string) (driver.Conn, error) {
	statusDBDSNMu.Lock()
	routes, ok := statusDBDSNMap[dsn]
	statusDBDSNMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake-status-db: no config for DSN %q", dsn)
	}
	return &statusDBConn{routes: routes}, nil
}

type statusDBConn struct{ routes *statusDBRoutes }

func (c *statusDBConn) Prepare(query string) (driver.Stmt, error) {
	return &statusDBStmt{routes: c.routes, query: query}, nil
}
func (c *statusDBConn) Close() error              { return nil }
func (c *statusDBConn) Begin() (driver.Tx, error) { return &statusDBTx{}, nil }

type statusDBTx struct{}

func (t *statusDBTx) Commit() error   { return nil }
func (t *statusDBTx) Rollback() error { return nil }

type statusDBStmt struct {
	routes *statusDBRoutes
	query  string
}

func (s *statusDBStmt) Close() error  { return nil }
func (s *statusDBStmt) NumInput() int { return -1 }
func (s *statusDBStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return statusDBResult{}, nil
}
func (s *statusDBStmt) Query(_ []driver.Value) (driver.Rows, error) {
	for _, route := range s.routes.routes {
		if strings.Contains(s.query, route.Contains) {
			if route.Err != nil {
				return nil, route.Err
			}
			return &statusDBRows{cols: route.Cols, rows: route.Rows}, nil
		}
	}
	return &statusDBRows{}, nil
}

type statusDBResult struct{}

func (r statusDBResult) LastInsertId() (int64, error) { return 0, nil }
func (r statusDBResult) RowsAffected() (int64, error) { return 0, nil }

type statusDBRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *statusDBRows) Columns() []string { return r.cols }
func (r *statusDBRows) Close() error      { return nil }
func (r *statusDBRows) Next(dest []driver.Value) error {
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

// ─── statusFromDB tests ───────────────────────────────────────────────────────

// TestStatusFromDB_EmptyTopology verifies that statusFromDB returns a valid
// StatusResult when all tables are empty (score == 1.0, trivially converged).
func TestStatusFromDB_EmptyTopology(t *testing.T) {
	// All queries return no rows — no routes needed, default empty result.
	db := newStatusDBFake(t, &statusDBRoutes{})
	ctx := context.Background()
	now := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)

	result, err := statusFromDB(ctx, db, StatusOptions{}, now)
	if err != nil {
		t.Fatalf("statusFromDB: %v", err)
	}
	if result == nil {
		t.Fatal("statusFromDB returned nil")
	}
	if result.Version != 1 {
		t.Errorf("Version = %d, want 1", result.Version)
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.3f, want 1.0 (empty topology is trivially converged)", result.Score)
	}
	if !result.ReadAt.Equal(now) {
		t.Errorf("ReadAt = %v, want %v", result.ReadAt, now)
	}
}

// TestStatusFromDB_FullyConvergedRig verifies that a single fully-converged
// rig produces the correct StatusResult fields.
func TestStatusFromDB_FullyConvergedRig(t *testing.T) {
	now := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-10 * time.Second)

	routes := &statusDBRoutes{routes: []statusDBRoute{
		{
			Contains: "actual_town",
			Cols:     []string{"name"},
			Rows:     [][]driver.Value{{"my-town"}},
		},
		{
			Contains: "desired_rigs",
			Cols:     []string{"name", "enabled", "max_polecats"},
			Rows:     [][]driver.Value{{"rig-a", true, int64(5)}},
		},
		{
			Contains: "actual_rigs",
			Cols:     []string{"name", "enabled", "status", "last_seen"},
			Rows:     [][]driver.Value{{"rig-a", true, "running", lastSeen}},
		},
		{
			Contains: "actual_agent_config",
			Cols:     []string{"rig_name", "role", "status", "last_seen"},
			Rows:     [][]driver.Value{{"rig-a", "mayor", "running", lastSeen}},
		},
	}}

	db := newStatusDBFake(t, routes)
	ctx := context.Background()

	result, err := statusFromDB(ctx, db, StatusOptions{}, now)
	if err != nil {
		t.Fatalf("statusFromDB: %v", err)
	}

	if result.Town != "my-town" {
		t.Errorf("Town = %q, want %q", result.Town, "my-town")
	}
	if result.Score != 1.0 {
		t.Errorf("Score = %.3f, want 1.0", result.Score)
	}
	if len(result.Rigs) != 1 {
		t.Fatalf("len(Rigs) = %d, want 1", len(result.Rigs))
	}
	rig := result.Rigs[0]
	if rig.Name != "rig-a" {
		t.Errorf("Rigs[0].Name = %q, want %q", rig.Name, "rig-a")
	}
	if rig.Score != 1.0 {
		t.Errorf("Rigs[0].Score = %.3f, want 1.0", rig.Score)
	}
	if rig.Status != "running" {
		t.Errorf("Rigs[0].Status = %q, want running", rig.Status)
	}
	if rig.PoolDesired != 5 {
		t.Errorf("Rigs[0].PoolDesired = %d, want 5", rig.PoolDesired)
	}
}

// TestStatusFromDB_RigFilter verifies that opts.Rigs restricts output to the
// named rigs only.
func TestStatusFromDB_RigFilter(t *testing.T) {
	now := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-10 * time.Second)

	routes := &statusDBRoutes{routes: []statusDBRoute{
		{
			Contains: "desired_rigs",
			Cols:     []string{"name", "enabled", "max_polecats"},
			Rows: [][]driver.Value{
				{"rig-a", true, int64(5)},
				{"rig-b", true, int64(3)},
			},
		},
		{
			Contains: "actual_rigs",
			Cols:     []string{"name", "enabled", "status", "last_seen"},
			Rows: [][]driver.Value{
				{"rig-a", true, "running", lastSeen},
				{"rig-b", true, "running", lastSeen},
			},
		},
		{
			Contains: "actual_agent_config",
			Cols:     []string{"rig_name", "role", "status", "last_seen"},
			Rows: [][]driver.Value{
				{"rig-a", "mayor", "running", lastSeen},
				{"rig-b", "mayor", "running", lastSeen},
			},
		},
	}}

	db := newStatusDBFake(t, routes)
	ctx := context.Background()

	result, err := statusFromDB(ctx, db, StatusOptions{Rigs: []string{"rig-a"}}, now)
	if err != nil {
		t.Fatalf("statusFromDB: %v", err)
	}

	if len(result.Rigs) != 1 {
		t.Fatalf("len(Rigs) = %d after filter, want 1", len(result.Rigs))
	}
	if result.Rigs[0].Name != "rig-a" {
		t.Errorf("Rigs[0].Name = %q, want rig-a", result.Rigs[0].Name)
	}
}

// ─── FormatStatusText tests ───────────────────────────────────────────────────

// TestStatusFormatText_ConvergedFleet verifies that all-converged rigs produce
// the ✓ icon and no ANSI codes when NoColor is true.
func TestStatusFormatText_ConvergedFleet(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   1.0,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "running", Score: 1.0, PoolDesired: 5, PoolActual: 2},
		},
	}

	out := FormatStatusText(r, FormatOpts{NoColor: true})

	if !strings.Contains(out, "✓") {
		t.Errorf("expected ✓ icon for fully converged fleet, got:\n%s", out)
	}
	if strings.Contains(out, "\033[") {
		t.Errorf("expected no ANSI codes when NoColor=true, got:\n%s", out)
	}
	if !strings.Contains(out, "my-town") {
		t.Errorf("expected town name in output, got:\n%s", out)
	}
}

// TestStatusFormatText_PartiallyConvergedFleet verifies that a rig with
// 0 < score < 1.0 produces the ⚠ icon.
func TestStatusFormatText_PartiallyConvergedFleet(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   0.6,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "running", Score: 0.6},
		},
	}

	out := FormatStatusText(r, FormatOpts{NoColor: true})
	if !strings.Contains(out, "⚠") {
		t.Errorf("expected ⚠ icon for partial convergence, got:\n%s", out)
	}
}

// TestStatusFormatText_UnconvergedFleet verifies that a rig with score == 0.0
// produces the ✗ icon.
func TestStatusFormatText_UnconvergedFleet(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   0.0,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "failed", Score: 0.0},
		},
	}

	out := FormatStatusText(r, FormatOpts{NoColor: true})
	if !strings.Contains(out, "✗") {
		t.Errorf("expected ✗ icon for fully unconverged fleet, got:\n%s", out)
	}
}

// TestStatusFormatText_StaleRig verifies that a rig with MayorStaleSeconds > 0
// shows the stale indicator in the output.
func TestStatusFormatText_StaleRig(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   0.8,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "running", Score: 0.8, MayorStaleSeconds: 120},
		},
	}

	out := FormatStatusText(r, FormatOpts{NoColor: true})
	if !strings.Contains(out, "STALE") {
		t.Errorf("expected STALE in output for stale mayor, got:\n%s", out)
	}
}

// TestStatusFormatText_ANSIPresent_WhenColorEnabled verifies that ANSI colour
// codes appear when NoColor is false and a rig score warrants colouring.
func TestStatusFormatText_ANSIPresent_WhenColorEnabled(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   0.0,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "failed", Score: 0.0},
		},
	}

	out := FormatStatusText(r, FormatOpts{NoColor: false})
	if !strings.Contains(out, "\033[") {
		t.Errorf("expected ANSI codes when NoColor=false and score=0.0, got:\n%s", out)
	}
}

// TestStatusFormatText_ANSIAbsent_WhenNoColor verifies that ANSI codes are
// suppressed when NoColor is true even for failing rigs.
func TestStatusFormatText_ANSIAbsent_WhenNoColor(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   0.0,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "failed", Score: 0.0},
		},
	}

	out := FormatStatusText(r, FormatOpts{NoColor: true})
	if strings.Contains(out, "\033[") {
		t.Errorf("expected no ANSI codes when NoColor=true, got output containing escape sequences")
	}
}

// ─── FormatStatusJSON tests ───────────────────────────────────────────────────

// TestStatusFormatJSON_ValidJSON_Version1 verifies that FormatStatusJSON
// produces parseable JSON with version == 1.
func TestStatusFormatJSON_ValidJSON_Version1(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "my-town",
		Score:   0.75,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{Name: "rig-a", Status: "running", Score: 0.75, PoolDesired: 5, PoolActual: 3},
		},
		Cost: []CostResult{
			{Rig: "rig-a", SpendUSD: 1.5, BudgetUSD: 10.0, Pct: 15.0},
		},
		OpenBeads: []BeadsSummaryResult{
			{Type: "bug", Priority: 1, Count: 2, OldestSeconds: 3600},
		},
	}

	b, err := FormatStatusJSON(r)
	if err != nil {
		t.Fatalf("FormatStatusJSON: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal JSON output: %v\noutput: %s", err, b)
	}

	if v, ok := decoded["version"].(float64); !ok || int(v) != 1 {
		t.Errorf("version = %v, want 1", decoded["version"])
	}
	if decoded["town"] != "my-town" {
		t.Errorf("town = %v, want my-town", decoded["town"])
	}
	if _, ok := decoded["rigs"]; !ok {
		t.Error("missing rigs field in JSON output")
	}
	if _, ok := decoded["cost"]; !ok {
		t.Error("missing cost field in JSON output")
	}
	if _, ok := decoded["open_beads"]; !ok {
		t.Error("missing open_beads field in JSON output")
	}
}

// TestStatusFormatJSON_CorrectRigFields verifies the JSON rig sub-object has
// the expected fields populated.
func TestStatusFormatJSON_CorrectRigFields(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Town:    "t",
		Score:   0.5,
		ReadAt:  time.Now(),
		Rigs: []RigStatusResult{
			{
				Name:              "rig-x",
				Status:            "running",
				Score:             0.5,
				MayorStaleSeconds: 0,
				PoolDesired:       10,
				PoolActual:        4,
				NonConverged:      []string{"pool/rig-x: not converged"},
			},
		},
	}

	b, err := FormatStatusJSON(r)
	if err != nil {
		t.Fatalf("FormatStatusJSON: %v", err)
	}

	var decoded struct {
		Rigs []struct {
			Name         string   `json:"name"`
			Score        float64  `json:"score"`
			PoolDesired  int      `json:"pool_desired"`
			NonConverged []string `json:"non_converged"`
		} `json:"rigs"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Rigs) != 1 {
		t.Fatalf("len(rigs) = %d, want 1", len(decoded.Rigs))
	}
	rig := decoded.Rigs[0]
	if rig.Name != "rig-x" {
		t.Errorf("name = %q, want rig-x", rig.Name)
	}
	if rig.Score != 0.5 {
		t.Errorf("score = %.2f, want 0.5", rig.Score)
	}
	if rig.PoolDesired != 10 {
		t.Errorf("pool_desired = %d, want 10", rig.PoolDesired)
	}
	if len(rig.NonConverged) != 1 {
		t.Errorf("non_converged len = %d, want 1", len(rig.NonConverged))
	}
}

// ─── StatusExitCode tests ─────────────────────────────────────────────────────

// TestStatusExitCode_ReturnsZeroWhenAllConverged verifies that exit code 0 is
// returned when all rig scores are 1.0.
func TestStatusExitCode_ReturnsZeroWhenAllConverged(t *testing.T) {
	r := &StatusResult{
		Version: 1,
		Rigs: []RigStatusResult{
			{Name: "rig-a", Score: 1.0},
			{Name: "rig-b", Score: 1.0},
		},
	}
	if got := StatusExitCode(r); got != 0 {
		t.Errorf("StatusExitCode = %d, want 0", got)
	}
}

// TestStatusExitCode_ReturnsTwoWhenAnyScoreLt1 verifies that exit code 2 is
// returned as soon as any rig has a score < 1.0.
func TestStatusExitCode_ReturnsTwoWhenAnyScoreLt1(t *testing.T) {
	tests := []struct {
		name string
		rigs []RigStatusResult
	}{
		{"single rig at 0", []RigStatusResult{{Name: "rig-a", Score: 0.0}}},
		{"partial convergence", []RigStatusResult{{Name: "rig-a", Score: 0.8}}},
		{"mixed — one failing", []RigStatusResult{
			{Name: "rig-a", Score: 1.0},
			{Name: "rig-b", Score: 0.5},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &StatusResult{Version: 1, Rigs: tc.rigs}
			if got := StatusExitCode(r); got != 2 {
				t.Errorf("StatusExitCode = %d, want 2", got)
			}
		})
	}
}

// TestStatusExitCode_ReturnsZeroWhenNoRigs verifies that an empty rig list
// returns exit code 0 (no rigs to check → trivially converged).
func TestStatusExitCode_ReturnsZeroWhenNoRigs(t *testing.T) {
	r := &StatusResult{Version: 1}
	if got := StatusExitCode(r); got != 0 {
		t.Errorf("StatusExitCode (no rigs) = %d, want 0", got)
	}
}
