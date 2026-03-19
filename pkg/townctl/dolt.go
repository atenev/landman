// Package townctl implements the town-ctl actuator logic for applying Gas Town
// topology manifests to Dolt (ADR-0001, ADR-0006).
//
// This file provides the Dolt database connection and transaction execution
// helpers used by the apply pipeline.
package townctl

import (
	"database/sql"
	"fmt"

	// Register the MySQL driver for Dolt's MySQL-wire protocol.
	_ "github.com/go-sql-driver/mysql"
)

// DB wraps a *sql.DB for Dolt-specific operations.
type DB struct {
	*sql.DB
}

// Connect opens a MySQL-protocol connection to Dolt and verifies connectivity.
// Dolt accepts standard MySQL driver DSN format. The caller must call Close()
// when done.
func Connect(host string, port int, dbName, user, password string) (*DB, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
		user, password, host, port, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("dolt: connect: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("dolt: connect: %w", err)
	}
	return &DB{db}, nil
}

// ExecTransaction executes stmts inside a single BEGIN / COMMIT. On any error
// the transaction is rolled back and the failing statement is included in the
// returned error. Per ADR-0003, the first statement must always be the
// desired_topology_versions upsert.
func (d *DB) ExecTransaction(stmts []string) error {
	tx, err := d.Begin()
	if err != nil {
		return fmt.Errorf("dolt: begin transaction: %w", err)
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("dolt: %s: %w", stmt, err)
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
	_, err := tx.Exec(fmt.Sprintf("SET @dolt_transaction_commit_message = '%s';",
		escapeSQLString(msg)))
	return err
}
