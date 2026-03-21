package controllers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// ── Fake SQL driver ───────────────────────────────────────────────────────────
//
// A minimal database/sql/driver that captures executed SQL statements and
// returns fake success results. Used to inject a Dolt mock into reconciler
// tests via the ConnectDolt factory field.

const fakeDoltDriverName = "fake-dolt"

var registerOnce sync.Once

func init() {
	registerOnce.Do(func() {
		sql.Register(fakeDoltDriverName, &fakeDoltDriver{})
	})
}

type fakeDoltDriver struct{}

func (d *fakeDoltDriver) Open(_ string) (driver.Conn, error) {
	return &fakeDoltConn{}, nil
}

type fakeDoltConn struct{}

func (c *fakeDoltConn) Prepare(query string) (driver.Stmt, error) {
	return &fakeDoltStmt{query: query}, nil
}

func (c *fakeDoltConn) Close() error        { return nil }
func (c *fakeDoltConn) Begin() (driver.Tx, error) { return &fakeDoltTx{}, nil }

type fakeDoltTx struct{}

func (t *fakeDoltTx) Commit() error   { return nil }
func (t *fakeDoltTx) Rollback() error { return nil }

type fakeDoltStmt struct{ query string }

func (s *fakeDoltStmt) Close() error                                  { return nil }
func (s *fakeDoltStmt) NumInput() int                                 { return -1 }
func (s *fakeDoltStmt) Exec(_ []driver.Value) (driver.Result, error) { return fakeDoltResult{}, nil }
func (s *fakeDoltStmt) Query(_ []driver.Value) (driver.Rows, error) {
	// SELECT dolt_hashof('HEAD') → return one row so reconcilers record a commit hash.
	// All other SELECT queries return an empty result set (sql.ErrNoRows path),
	// which the reconcilers treat as "not yet seen" — safe for test isolation.
	cols := parseSelectColumns(s.query)
	if strContains(s.query, "dolt_hashof") {
		return &fakeDoltRows{cols: cols, values: []string{"abc123def456"}}, nil
	}
	return &fakeDoltRows{cols: cols}, nil
}

// parseSelectColumns extracts column names from a SQL SELECT statement.
// It is used only in tests to provide accurate Columns() metadata for the
// fake SQL driver without executing real SQL.
func parseSelectColumns(query string) []string {
	q := strings.ToUpper(query)
	sel := strings.Index(q, "SELECT ")
	if sel == -1 {
		return []string{"col"}
	}
	start := sel + 7
	end := strings.Index(q[start:], " FROM ")
	var part string
	if end == -1 {
		part = strings.TrimSpace(query[start:])
	} else {
		part = strings.TrimSpace(query[start : start+end])
	}
	parts := strings.Split(part, ",")
	cols := make([]string, 0, len(parts))
	for _, c := range parts {
		c = strings.TrimSpace(c)
		if i := strings.LastIndex(strings.ToUpper(c), " AS "); i != -1 {
			c = strings.TrimSpace(c[i+4:])
		}
		if c != "" {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return []string{"col"}
	}
	return cols
}

// strContains is a simple substring search used only in tests.
func strContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

type fakeDoltResult struct{}

func (r fakeDoltResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeDoltResult) RowsAffected() (int64, error) { return 1, nil }

type fakeDoltRows struct {
	cols   []string
	values []string
	pos    int
}

func (r *fakeDoltRows) Columns() []string {
	if len(r.cols) == 0 {
		return []string{"col"}
	}
	return r.cols
}
func (r *fakeDoltRows) Close() error      { return nil }
func (r *fakeDoltRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	dest[0] = r.values[r.pos]
	r.pos++
	return nil
}

// ── Factory helpers ───────────────────────────────────────────────────────────

// newFakeDoltDB returns a *sql.DB backed by the in-process fake driver.
func newFakeDoltDB() *sql.DB {
	db, err := sql.Open(fakeDoltDriverName, "")
	if err != nil {
		panic(fmt.Sprintf("fake-dolt: sql.Open: %v", err))
	}
	return db
}

// fakeDoltConnector returns a DoltConnector that always returns the given db
// without touching the Kubernetes API or dialling any TCP endpoint.
func fakeDoltConnector(db *sql.DB) DoltConnector {
	return func(_ context.Context, _ client.Client, _ gasv1alpha1.NamespacedRef) (*doltClient, error) {
		return &doltClient{db: db}, nil
	}
}

// fakeDoltConnectorByName returns a DoltConnectorByName backed by db.
func fakeDoltConnectorByName(db *sql.DB) DoltConnectorByName {
	return func(_ context.Context, _ client.Client, _, _ string) (*doltClient, error) {
		return &doltClient{db: db}, nil
	}
}

// ── Capturing fake SQL driver ─────────────────────────────────────────────────
//
// newCapturingFakeDoltDB returns a *sql.DB whose executed statements are
// captured in a queryCapture for assertion in tests. Use anyQuery to check
// that a SQL template substring was executed, and containsStringArg to check
// that a statement with a given query substring also had a matching string arg.

// executedStmt records one SQL Exec call.
type executedStmt struct {
	query string
	args  []driver.Value
}

// queryCapture accumulates SQL statements executed against a capturing fake DB.
type queryCapture struct {
	mu    sync.Mutex
	stmts []executedStmt
}

func (c *queryCapture) record(query string, args []driver.Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]driver.Value, len(args))
	copy(cp, args)
	c.stmts = append(c.stmts, executedStmt{query: query, args: cp})
}

// anyQuery returns true if any captured statement's query contains sub.
func (c *queryCapture) anyQuery(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.stmts {
		if strings.Contains(s.query, sub) {
			return true
		}
	}
	return false
}

// containsStringArg returns true if any captured statement whose query contains
// querySub also passed a string argument containing argSub.
func (c *queryCapture) containsStringArg(querySub, argSub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.stmts {
		if !strings.Contains(s.query, querySub) {
			continue
		}
		for _, v := range s.args {
			if sv, ok := v.(string); ok && strings.Contains(sv, argSub) {
				return true
			}
		}
	}
	return false
}

type capturingFakeDoltStmt struct {
	query   string
	capture *queryCapture
}

func (s *capturingFakeDoltStmt) Close() error  { return nil }
func (s *capturingFakeDoltStmt) NumInput() int { return -1 }
func (s *capturingFakeDoltStmt) Exec(args []driver.Value) (driver.Result, error) {
	s.capture.record(s.query, args)
	return fakeDoltResult{}, nil
}
func (s *capturingFakeDoltStmt) Query(args []driver.Value) (driver.Rows, error) {
	cols := parseSelectColumns(s.query)
	if strContains(s.query, "dolt_hashof") {
		return &fakeDoltRows{cols: cols, values: []string{"abc123def456"}}, nil
	}
	return &fakeDoltRows{cols: cols}, nil
}

type capturingFakeDoltConn struct {
	capture *queryCapture
}

func (c *capturingFakeDoltConn) Prepare(query string) (driver.Stmt, error) {
	return &capturingFakeDoltStmt{query: query, capture: c.capture}, nil
}
func (c *capturingFakeDoltConn) Close() error                      { return nil }
func (c *capturingFakeDoltConn) Begin() (driver.Tx, error)         { return &fakeDoltTx{}, nil }

type capturingDoltConnector struct {
	capture *queryCapture
}

func (c *capturingDoltConnector) Connect(_ context.Context) (driver.Conn, error) {
	return &capturingFakeDoltConn{capture: c.capture}, nil
}
func (c *capturingDoltConnector) Driver() driver.Driver { return &fakeDoltDriver{} }

// newCapturingFakeDoltDB returns a *sql.DB backed by the capturing fake driver.
func newCapturingFakeDoltDB() (*sql.DB, *queryCapture) {
	cap := &queryCapture{}
	return sql.OpenDB(&capturingDoltConnector{capture: cap}), cap
}
