// Package townctl_test — unit tests for CheckTopologyLock, ExecTransaction,
// and TopologyLockUpsertSQL (dgt-pgo).
//
// These tests use a minimal in-process fake database/sql driver so that no
// Dolt or MySQL connection is required.
package townctl_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tenev/dgt/pkg/townctl"
)

// ─── Fake SQL Driver ─────────────────────────────────────────────────────────
//
// Minimal database/sql driver for unit testing.  Registered once as "fakesql";
// per-test behaviour is configured via fakeConnCfg and the DSN registry.

var (
	fakeDSNMu   sync.Mutex
	fakeDSNMap  = map[string]*fakeConnCfg{}
	fakeDSNCtr  int64
	fakeSQLOnce sync.Once
)

// fakeQueryRow holds the single row to return from a Query call.
// A nil *fakeQueryRow means "return no rows" (causes sql.ErrNoRows).
type fakeQueryRow struct {
	cols []string
	vals []driver.Value
}

// fakeConnCfg configures the fake connection's behaviour for one test.
type fakeConnCfg struct {
	queryRow  *fakeQueryRow // nil → ErrNoRows on Scan
	execErrAt int           // 0-based index of Exec call that should fail; -1 = never
	execErr   error         // error returned at execErrAt
	commitErr error         // error returned by Commit
	beginErr  error         // error returned by Begin
}

func registerFakeDriver() {
	fakeSQLOnce.Do(func() { sql.Register("fakesql", fakeDriver{}) })
}

// newFakeDB creates a *townctl.DB backed by the fake driver with cfg.
// It registers the config, opens a single-connection pool, and cleans up on t.
func newFakeDB(t *testing.T, cfg *fakeConnCfg) *townctl.DB {
	t.Helper()
	registerFakeDriver()

	dsn := fmt.Sprintf("fake-%d", atomic.AddInt64(&fakeDSNCtr, 1))
	fakeDSNMu.Lock()
	fakeDSNMap[dsn] = cfg
	fakeDSNMu.Unlock()
	t.Cleanup(func() {
		fakeDSNMu.Lock()
		delete(fakeDSNMap, dsn)
		fakeDSNMu.Unlock()
	})

	sqlDB, err := sql.Open("fakesql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// One connection so that all operations in a transaction share the same
	// fakeConn and the execCount tracks correctly.
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })

	return &townctl.DB{DB: sqlDB}
}

// ─── driver.Driver ───────────────────────────────────────────────────────────

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	fakeDSNMu.Lock()
	cfg, ok := fakeDSNMap[dsn]
	fakeDSNMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fakesql: no config for DSN %q", dsn)
	}
	return &fakeConn{cfg: cfg}, nil
}

// ─── driver.Conn ─────────────────────────────────────────────────────────────

type fakeConn struct {
	cfg       *fakeConnCfg
	execCount int
	mu        sync.Mutex
}

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return &fakeStmt{conn: c, query: query}, nil
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) Begin() (driver.Tx, error) {
	if c.cfg.beginErr != nil {
		return nil, c.cfg.beginErr
	}
	return &fakeTx{conn: c}, nil
}

// ─── driver.Stmt ─────────────────────────────────────────────────────────────

type fakeStmt struct {
	conn  *fakeConn
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 } // variadic

func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.conn.mu.Lock()
	idx := s.conn.execCount
	s.conn.execCount++
	s.conn.mu.Unlock()

	if s.conn.cfg.execErrAt >= 0 && idx == s.conn.cfg.execErrAt {
		return nil, s.conn.cfg.execErr
	}
	return fakeResult{}, nil
}

func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.conn.cfg.queryRow == nil {
		return &fakeRows{}, nil
	}
	return &fakeRows{
		cols:    s.conn.cfg.queryRow.cols,
		vals:    s.conn.cfg.queryRow.vals,
		pending: true,
	}, nil
}

// ─── driver.Rows ─────────────────────────────────────────────────────────────

type fakeRows struct {
	cols    []string
	vals    []driver.Value
	pending bool // true until first Next call
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if !r.pending {
		return io.EOF
	}
	r.pending = false
	copy(dest, r.vals)
	return nil
}

// ─── driver.Result ───────────────────────────────────────────────────────────

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

// ─── driver.Tx ───────────────────────────────────────────────────────────────

type fakeTx struct{ conn *fakeConn }

func (tx *fakeTx) Commit() error   { return tx.conn.cfg.commitErr }
func (tx *fakeTx) Rollback() error { return nil }

// ─── TopologyLockUpsertSQL ────────────────────────────────────────────────────

func TestTopologyLockUpsertSQL_InsertsIntoCorrectTable(t *testing.T) {
	stmt := townctl.TopologyLockUpsertSQL("town-ctl/dev")
	if !strings.Contains(stmt.Query, "INSERT INTO desired_topology_lock") {
		t.Errorf("expected INSERT INTO desired_topology_lock, got: %s", stmt.Query)
	}
}

func TestTopologyLockUpsertSQL_IsIdempotentUpsert(t *testing.T) {
	stmt := townctl.TopologyLockUpsertSQL("town-ctl/dev")
	if !strings.Contains(stmt.Query, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("expected ON DUPLICATE KEY UPDATE for idempotent upsert, got: %s", stmt.Query)
	}
}

func TestTopologyLockUpsertSQL_HolderPassedAsArg_NotInQuery(t *testing.T) {
	const holder = "town-ctl/1.2.3"
	stmt := townctl.TopologyLockUpsertSQL(holder)
	if len(stmt.Args) == 0 || stmt.Args[0] != holder {
		t.Errorf("expected holder %q as first arg, got args: %v", holder, stmt.Args)
	}
	if strings.Contains(stmt.Query, holder) {
		t.Errorf("holder must not be embedded in query literal (SQL injection risk); got: %s",
			stmt.Query)
	}
}

// ─── CheckTopologyLock ────────────────────────────────────────────────────────

func TestCheckTopologyLock_NoRow_ReturnsNil(t *testing.T) {
	// nil queryRow → fakeRows returns io.EOF immediately → sql.ErrNoRows
	db := newFakeDB(t, &fakeConnCfg{queryRow: nil, execErrAt: -1})
	if err := townctl.CheckTopologyLock(db, "town-ctl/dev"); err != nil {
		t.Errorf("expected nil when no lock row, got: %v", err)
	}
}

func TestCheckTopologyLock_LockBySameHolder_ReturnsNil(t *testing.T) {
	const holder = "town-ctl/dev"
	db := newFakeDB(t, &fakeConnCfg{
		queryRow: &fakeQueryRow{
			cols: []string{"holder", "acquired_at"},
			vals: []driver.Value{holder, time.Now()},
		},
		execErrAt: -1,
	})
	if err := townctl.CheckTopologyLock(db, holder); err != nil {
		t.Errorf("expected nil when lock held by same holder, got: %v", err)
	}
}

func TestCheckTopologyLock_LockByDifferentHolder_ActiveTTL_ReturnsError(t *testing.T) {
	const holder = "town-ctl/dev"
	db := newFakeDB(t, &fakeConnCfg{
		queryRow: &fakeQueryRow{
			cols: []string{"holder", "acquired_at"},
			vals: []driver.Value{"k8s-operator/1.0.0", time.Now()},
		},
		execErrAt: -1,
	})
	err := townctl.CheckTopologyLock(db, holder)
	if err == nil {
		t.Fatal("expected error when lock held by different holder within TTL, got nil")
	}
	if !strings.Contains(err.Error(), "k8s-operator/1.0.0") {
		t.Errorf("error should mention current holder %q, got: %v", "k8s-operator/1.0.0", err)
	}
}

func TestCheckTopologyLock_LockByDifferentHolder_ExpiredTTL_ReturnsNil(t *testing.T) {
	const holder = "town-ctl/dev"
	// 60s ago — safely past the 30s topologyLockTTL.
	db := newFakeDB(t, &fakeConnCfg{
		queryRow: &fakeQueryRow{
			cols: []string{"holder", "acquired_at"},
			vals: []driver.Value{"k8s-operator/1.0.0", time.Now().Add(-60 * time.Second)},
		},
		execErrAt: -1,
	})
	if err := townctl.CheckTopologyLock(db, holder); err != nil {
		t.Errorf("expected nil for expired lock (different holder), got: %v", err)
	}
}

// ─── ExecTransaction ─────────────────────────────────────────────────────────

func TestExecTransaction_EmptyStmts_ReturnsNil(t *testing.T) {
	db := newFakeDB(t, &fakeConnCfg{execErrAt: -1})
	if err := db.ExecTransaction(nil); err != nil {
		t.Errorf("expected nil for empty statement list, got: %v", err)
	}
}

func TestExecTransaction_AllSucceed_ReturnsNil(t *testing.T) {
	db := newFakeDB(t, &fakeConnCfg{execErrAt: -1})
	stmts := []townctl.Stmt{
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"a"}},
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"b"}},
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"c"}},
	}
	if err := db.ExecTransaction(stmts); err != nil {
		t.Errorf("expected nil for all-succeed transaction, got: %v", err)
	}
}

func TestExecTransaction_FirstStmtFails_ReturnsError(t *testing.T) {
	db := newFakeDB(t, &fakeConnCfg{
		execErrAt: 0,
		execErr:   fmt.Errorf("syntax error near 'BAD'"),
	})
	stmts := []townctl.Stmt{
		{Query: "BAD SQL;"},
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"b"}},
	}
	err := db.ExecTransaction(stmts)
	if err == nil {
		t.Fatal("expected error when first statement fails, got nil")
	}
	if !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("error should wrap original message, got: %v", err)
	}
}

func TestExecTransaction_SecondStmtFails_ReturnsError(t *testing.T) {
	db := newFakeDB(t, &fakeConnCfg{
		execErrAt: 1,
		execErr:   fmt.Errorf("constraint violation"),
	})
	stmts := []townctl.Stmt{
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"a"}},
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"b"}},
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"c"}},
	}
	err := db.ExecTransaction(stmts)
	if err == nil {
		t.Fatal("expected error when second statement fails, got nil")
	}
	if !strings.Contains(err.Error(), "constraint violation") {
		t.Errorf("error should wrap original message, got: %v", err)
	}
}

func TestExecTransaction_CommitFails_ReturnsError(t *testing.T) {
	db := newFakeDB(t, &fakeConnCfg{
		execErrAt: -1,
		commitErr: fmt.Errorf("commit failed: disk full"),
	})
	stmts := []townctl.Stmt{
		{Query: "INSERT INTO t VALUES (?);", Args: []any{"a"}},
	}
	err := db.ExecTransaction(stmts)
	if err == nil {
		t.Fatal("expected error when commit fails, got nil")
	}
	if !strings.Contains(err.Error(), "commit failed") {
		t.Errorf("error should wrap commit failure, got: %v", err)
	}
}
