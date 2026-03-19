package controllers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
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

type fakeDoltConn struct {
	mu      sync.Mutex
	queries []string
}

func (c *fakeDoltConn) Prepare(query string) (driver.Stmt, error) {
	c.mu.Lock()
	c.queries = append(c.queries, query)
	c.mu.Unlock()
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
	if strContains(s.query, "dolt_hashof") {
		return &fakeDoltRows{values: []string{"abc123def456"}, pos: 0}, nil
	}
	return &fakeDoltRows{values: nil, pos: 0}, nil
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
	values []string
	pos    int
}

func (r *fakeDoltRows) Columns() []string { return []string{"val"} }
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
