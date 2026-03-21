// Package observer_test — unit tests for ReadBeads (dgt-358).
//
// Uses a routing fake SQL driver to exercise ReadBeads without a real Dolt
// connection. The driver matches query substrings to per-test row sets,
// mirroring the pattern established in pkg/townctl/status_test.go.
package observer_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tenev/dgt/pkg/observer"
)

// ─── routing fake SQL driver ──────────────────────────────────────────────────

var (
	beadsDBDriverOnce sync.Once
	beadsDBDSNMu      sync.Mutex
	beadsDBDSNMap     = map[string]*beadsDBRoutes{}
	beadsDBDSNCtr     atomic.Int64
)

// beadsDBRoute maps a SQL substring to a result set or an error.
type beadsDBRoute struct {
	Contains string
	Cols     []string
	Rows     [][]driver.Value
	Err      error
}

type beadsDBRoutes struct {
	routes []beadsDBRoute
}

func registerBeadsDBDriver() {
	beadsDBDriverOnce.Do(func() {
		sql.Register("fake-beads-db", &beadsDBDriver{})
	})
}

// newBeadsDBFake creates a *sql.DB backed by the routing fake.
func newBeadsDBFake(t *testing.T, routes *beadsDBRoutes) *sql.DB {
	t.Helper()
	registerBeadsDBDriver()

	dsn := fmt.Sprintf("beadsdb-fake-%d", beadsDBDSNCtr.Add(1))
	beadsDBDSNMu.Lock()
	beadsDBDSNMap[dsn] = routes
	beadsDBDSNMu.Unlock()
	t.Cleanup(func() {
		beadsDBDSNMu.Lock()
		delete(beadsDBDSNMap, dsn)
		beadsDBDSNMu.Unlock()
	})

	db, err := sql.Open("fake-beads-db", dsn)
	if err != nil {
		t.Fatalf("sql.Open fake-beads-db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// ─── driver.Driver ────────────────────────────────────────────────────────────

type beadsDBDriver struct{}

func (d *beadsDBDriver) Open(dsn string) (driver.Conn, error) {
	beadsDBDSNMu.Lock()
	routes, ok := beadsDBDSNMap[dsn]
	beadsDBDSNMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake-beads-db: no config for DSN %q", dsn)
	}
	return &beadsDBConn{routes: routes}, nil
}

type beadsDBConn struct{ routes *beadsDBRoutes }

func (c *beadsDBConn) Prepare(query string) (driver.Stmt, error) {
	return &beadsDBStmt{routes: c.routes, query: query}, nil
}
func (c *beadsDBConn) Close() error              { return nil }
func (c *beadsDBConn) Begin() (driver.Tx, error) { return &beadsDBTx{}, nil }

type beadsDBTx struct{}

func (t *beadsDBTx) Commit() error   { return nil }
func (t *beadsDBTx) Rollback() error { return nil }

type beadsDBStmt struct {
	routes *beadsDBRoutes
	query  string
}

func (s *beadsDBStmt) Close() error  { return nil }
func (s *beadsDBStmt) NumInput() int { return -1 }
func (s *beadsDBStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return beadsDBResult{}, nil
}
func (s *beadsDBStmt) Query(_ []driver.Value) (driver.Rows, error) {
	for _, route := range s.routes.routes {
		if strings.Contains(s.query, route.Contains) {
			if route.Err != nil {
				return nil, route.Err
			}
			return &beadsDBRows{cols: route.Cols, rows: route.Rows}, nil
		}
	}
	return &beadsDBRows{}, nil
}

type beadsDBResult struct{}

func (r beadsDBResult) LastInsertId() (int64, error) { return 0, nil }
func (r beadsDBResult) RowsAffected() (int64, error) { return 0, nil }

type beadsDBRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *beadsDBRows) Columns() []string { return r.cols }
func (r *beadsDBRows) Close() error      { return nil }
func (r *beadsDBRows) Next(dest []driver.Value) error {
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

// ─── tests ────────────────────────────────────────────────────────────────────

// TestReadBeads_OpenCountsReturnedCorrectly verifies that ReadBeads populates
// OpenByTypePriority with the counts returned by the open-issue query.
func TestReadBeads_OpenCountsReturnedCorrectly(t *testing.T) {
	t.Parallel()
	db := newBeadsDBFake(t, &beadsDBRoutes{
		routes: []beadsDBRoute{
			{
				// open/in_progress count query
				Contains: "status IN",
				Cols:     []string{"type", "priority", "COUNT(*)"},
				Rows: [][]driver.Value{
					{"task", int64(1), int64(3)},
					{"bug", int64(0), int64(2)},
					{"feature", int64(2), int64(5)},
				},
			},
			{
				// latency query — return empty for this test
				Contains: "TIMESTAMPDIFF",
				Cols:     []string{"type", "TIMESTAMPDIFF(SECOND, created_at, closed_at)"},
			},
		},
	})

	snap, err := observer.ReadBeads(context.Background(), db, 30*time.Second)
	if err != nil {
		t.Fatalf("ReadBeads returned unexpected error: %v", err)
	}

	cases := []struct {
		key  observer.BeadsKey
		want int64
	}{
		{observer.BeadsKey{Type: "task", Priority: 1}, 3},
		{observer.BeadsKey{Type: "bug", Priority: 0}, 2},
		{observer.BeadsKey{Type: "feature", Priority: 2}, 5},
	}
	for _, tc := range cases {
		got, ok := snap.OpenByTypePriority[tc.key]
		if !ok {
			t.Errorf("OpenByTypePriority[%+v] missing", tc.key)
			continue
		}
		if got != tc.want {
			t.Errorf("OpenByTypePriority[%+v] = %d, want %d", tc.key, got, tc.want)
		}
	}
	if len(snap.OpenByTypePriority) != 3 {
		t.Errorf("OpenByTypePriority len = %d, want 3", len(snap.OpenByTypePriority))
	}
}

// TestReadBeads_LatencySamplesWithinWindow verifies that ReadBeads correctly
// parses latency rows into RecentLatencies entries.
func TestReadBeads_LatencySamplesWithinWindow(t *testing.T) {
	t.Parallel()
	db := newBeadsDBFake(t, &beadsDBRoutes{
		routes: []beadsDBRoute{
			{
				Contains: "status IN",
				Cols:     []string{"type", "priority", "COUNT(*)"},
			},
			{
				Contains: "TIMESTAMPDIFF",
				Cols:     []string{"type", "TIMESTAMPDIFF(SECOND, created_at, closed_at)"},
				Rows: [][]driver.Value{
					{"task", int64(120)},
					{"bug", int64(3600)},
					{"feature", int64(300)},
				},
			},
		},
	})

	snap, err := observer.ReadBeads(context.Background(), db, 30*time.Second)
	if err != nil {
		t.Fatalf("ReadBeads returned unexpected error: %v", err)
	}

	if len(snap.RecentLatencies) != 3 {
		t.Fatalf("RecentLatencies len = %d, want 3", len(snap.RecentLatencies))
	}

	type want struct {
		issueType string
		seconds   float64
	}
	wantLatencies := []want{
		{"task", 120},
		{"bug", 3600},
		{"feature", 300},
	}
	for i, w := range wantLatencies {
		got := snap.RecentLatencies[i]
		if got.Type != w.issueType {
			t.Errorf("RecentLatencies[%d].Type = %q, want %q", i, got.Type, w.issueType)
		}
		if got.Seconds != w.seconds {
			t.Errorf("RecentLatencies[%d].Seconds = %v, want %v", i, got.Seconds, w.seconds)
		}
	}
}

// TestReadBeads_ClosedOutsideWindowExcluded verifies that when the database
// returns no latency rows (representing issues closed outside the window),
// RecentLatencies is empty and no error is returned.
func TestReadBeads_ClosedOutsideWindowExcluded(t *testing.T) {
	t.Parallel()
	db := newBeadsDBFake(t, &beadsDBRoutes{
		routes: []beadsDBRoute{
			{
				Contains: "status IN",
				Cols:     []string{"type", "priority", "COUNT(*)"},
				Rows: [][]driver.Value{
					{"task", int64(0), int64(1)},
				},
			},
			{
				// Latency query returns no rows: all closed issues are outside window.
				Contains: "TIMESTAMPDIFF",
				Cols:     []string{"type", "TIMESTAMPDIFF(SECOND, created_at, closed_at)"},
			},
		},
	})

	snap, err := observer.ReadBeads(context.Background(), db, 10*time.Second)
	if err != nil {
		t.Fatalf("ReadBeads returned unexpected error: %v", err)
	}
	if len(snap.RecentLatencies) != 0 {
		t.Errorf("RecentLatencies len = %d, want 0 (all outside window)", len(snap.RecentLatencies))
	}
}

// TestReadBeads_EmptyTableNoError verifies that ReadBeads handles an empty
// bd_issues table without error, returning an empty but valid snapshot.
func TestReadBeads_EmptyTableNoError(t *testing.T) {
	t.Parallel()
	db := newBeadsDBFake(t, &beadsDBRoutes{
		routes: []beadsDBRoute{
			{
				Contains: "status IN",
				Cols:     []string{"type", "priority", "COUNT(*)"},
			},
			{
				Contains: "TIMESTAMPDIFF",
				Cols:     []string{"type", "TIMESTAMPDIFF(SECOND, created_at, closed_at)"},
			},
		},
	})

	snap, err := observer.ReadBeads(context.Background(), db, 30*time.Second)
	if err != nil {
		t.Fatalf("ReadBeads returned unexpected error on empty table: %v", err)
	}
	if len(snap.OpenByTypePriority) != 0 {
		t.Errorf("OpenByTypePriority len = %d, want 0", len(snap.OpenByTypePriority))
	}
	if len(snap.RecentLatencies) != 0 {
		t.Errorf("RecentLatencies len = %d, want 0", len(snap.RecentLatencies))
	}
	if snap.ReadAt.IsZero() {
		t.Error("ReadAt is zero, want a non-zero timestamp")
	}
}
