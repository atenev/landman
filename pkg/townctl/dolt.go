// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file provides the Dolt database connection and transaction execution
// helpers used by the apply pipeline.
package townctl

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// Register the MySQL driver for Dolt's MySQL-wire protocol.
	_ "github.com/go-sql-driver/mysql"
)

// DB wraps a *sql.DB for Dolt-specific operations.
type DB struct {
	*sql.DB
}

// Stmt is a SQL statement with optional query arguments for parameterized
// execution. Use ? placeholders in Query and supply matching Args values.
type Stmt struct {
	Query string
	Args  []any
}

// ConnectDSN opens a MySQL-protocol connection to Dolt using a raw DSN string
// and verifies connectivity. ctx bounds the initial ping.
// The caller must call Close() when done.
func ConnectDSN(ctx context.Context, dsn string) (*DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("dolt: connect: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("dolt: connect: %w", err)
	}
	return &DB{db}, nil
}

// Connect opens a MySQL-protocol connection to Dolt and verifies connectivity.
// Dolt accepts standard MySQL driver DSN format. ctx bounds the initial ping;
// use context.WithTimeout to prevent indefinite hangs on misconfigured hosts.
// The caller must call Close() when done.
func Connect(ctx context.Context, host string, port int, dbName, user, password string) (*DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
		user, password, host, port, dbName)
	// maskedDSN replaces the password with *** for use in error messages so that
	// credentials are never written to logs or Kubernetes Events.
	maskedDSN := fmt.Sprintf("%s:***@tcp(%s:%d)/%s?parseTime=true",
		user, host, port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("dolt: open %s: %w", maskedDSN, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("dolt: ping %s: %w", maskedDSN, err)
	}
	return &DB{db}, nil
}

// SQLSummary returns the first 60 characters of s followed by "..." if s is
// longer. Use this instead of embedding raw SQL in error messages to avoid
// leaking schema structure and filesystem paths into logs.
func SQLSummary(s string) string {
	const maxLen = 60
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ExecTransaction executes stmts inside a single BEGIN / COMMIT. On any error
// the transaction is rolled back and the failing statement is included in the
// returned error. Per ADR-0003, the first statement must always be the
// desired_topology_versions upsert.
func (d *DB) ExecTransaction(stmts []Stmt) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("dolt: begin transaction: %w", err)
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt.Query, stmt.Args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("dolt: %s: %w", SQLSummary(stmt.Query), err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("dolt: commit: %w", err)
	}
	return nil
}

// SetCommitMessage sets the Dolt transaction commit message that will be
// attached to the next COMMIT. This must be called inside a transaction,
// before Commit.
func SetCommitMessage(tx *sql.Tx, msg string) error {
	_, err := tx.Exec("SET @dolt_transaction_commit_message = ?", msg)
	return err
}

// topologyLockTTL is the window during which a lock held by a different
// component is considered "live" and causes CheckTopologyLock to fail.
const topologyLockTTL = 30 * time.Second

// CheckTopologyLock reads the desired_topology_lock sentinel row and returns
// an error if a different component holds the lock within topologyLockTTL.
// Call this before ExecTransaction to detect concurrent operator writes.
func CheckTopologyLock(db *DB, holder string) error {
	var currentHolder string
	var acquiredAt time.Time
	err := db.QueryRow(
		"SELECT holder, acquired_at FROM desired_topology_lock WHERE singleton = 'X'",
	).Scan(&currentHolder, &acquiredAt)
	if err == sql.ErrNoRows {
		return nil // no lock row yet — safe to write
	}
	if err != nil {
		return fmt.Errorf("topology lock check: %w", err)
	}
	if currentHolder != holder && time.Since(acquiredAt) < topologyLockTTL {
		return fmt.Errorf("desired topology locked by %q (%s ago); wait and retry",
			currentHolder, time.Since(acquiredAt).Round(time.Second))
	}
	return nil
}

// TopologyLockUpsertSQL returns a Stmt that claims the advisory topology write
// lock for holder. Include this as the last statement in the ExecTransaction
// stmts slice so the lock is updated atomically with the desired-state writes.
func TopologyLockUpsertSQL(holder string) Stmt {
	return Stmt{
		Query: "INSERT INTO desired_topology_lock (singleton, holder, acquired_at)" +
			" VALUES ('X', ?, NOW())" +
			" ON DUPLICATE KEY UPDATE holder = VALUES(holder), acquired_at = VALUES(acquired_at);",
		Args: []any{holder},
	}
}
