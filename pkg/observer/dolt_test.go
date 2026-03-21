package observer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/tenev/dgt/pkg/observer"
)

// ─── helpers ────────────────────────────────────────────────────────────────

// mustNewMock creates a sqlmock db and fails the test on error.
func mustNewMock(t *testing.T) (*sqlmock.Sqlmock, interface {
	Close() error
}) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	return &mock, db
}

// ─── ReadTopology: well-formed rows ─────────────────────────────────────────

func TestReadTopology_WellFormed_DesiredRigs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// desired_rigs
	mock.ExpectQuery(`SELECT name, enabled FROM desired_rigs`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "enabled"}).
			AddRow("rig-a", true).
			AddRow("rig-b", false))

	// desired_agent_config
	mock.ExpectQuery(`SELECT rig_name, role, enabled, max_polecats`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role", "enabled", "max_polecats"}).
			AddRow("rig-a", "polecat", true, 5).
			AddRow("rig-a", "witness", true, nil))

	// desired_custom_roles (town-scoped)
	mock.ExpectQuery(`SELECT name, max_instances`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "max_instances"}))

	// desired_rig_custom_roles (rig-scoped)
	mock.ExpectQuery(`SELECT cr\.name, rcr\.rig_name, cr\.max_instances`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "rig_name", "max_instances"}))

	// desired_formulas
	mock.ExpectQuery(`SELECT rig_name, name FROM desired_formulas`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "name"}).
			AddRow("rig-a", "nightly-build"))

	// actual_rigs
	now := time.Now()
	mock.ExpectQuery(`SELECT name, enabled, status, last_seen FROM actual_rigs`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "enabled", "status", "last_seen"}).
			AddRow("rig-a", true, "running", now))

	// actual_agent_config
	mock.ExpectQuery(`SELECT rig_name, role, status, last_seen FROM actual_agent_config`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role", "status", "last_seen"}).
			AddRow("rig-a", "mayor", "running", now))

	// actual_worktrees
	mock.ExpectQuery(`SELECT rig_name, status, last_seen FROM actual_worktrees`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "status", "last_seen"}).
			AddRow("rig-a", "active", now))

	// actual_custom_roles
	mock.ExpectQuery(`SELECT rig_name, role_name, instance_index, status, last_seen`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role_name", "instance_index", "status", "last_seen"}))

	snap, err := observer.ReadTopology(context.Background(), db)
	if err != nil {
		t.Fatalf("ReadTopology: unexpected error: %v", err)
	}

	// Desired rigs.
	if len(snap.Desired.Rigs) != 2 {
		t.Fatalf("desired rigs: got %d, want 2", len(snap.Desired.Rigs))
	}
	rigA := snap.Desired.Rigs[0]
	if rigA.Name != "rig-a" || !rigA.Enabled || rigA.MaxPolecats != 5 || !rigA.WitnessEnabled {
		t.Errorf("rig-a: got %+v", rigA)
	}
	rigB := snap.Desired.Rigs[1]
	if rigB.Name != "rig-b" || rigB.Enabled {
		t.Errorf("rig-b: got %+v", rigB)
	}

	// Desired formulas.
	if len(snap.Desired.Formulas) != 1 || snap.Desired.Formulas[0].Name != "nightly-build" {
		t.Errorf("desired formulas: got %v", snap.Desired.Formulas)
	}

	// Actual rigs.
	if len(snap.Actual.Rigs) != 1 || snap.Actual.Rigs[0].Name != "rig-a" {
		t.Errorf("actual rigs: got %v", snap.Actual.Rigs)
	}

	// Actual agents.
	if len(snap.Actual.Agents) != 1 || snap.Actual.Agents[0].Role != "mayor" {
		t.Errorf("actual agents: got %v", snap.Actual.Agents)
	}

	// Actual worktrees.
	if len(snap.Actual.Worktrees) != 1 || snap.Actual.Worktrees[0].Status != "active" {
		t.Errorf("actual worktrees: got %v", snap.Actual.Worktrees)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

// ─── ReadTopology: error isolation ──────────────────────────────────────────

// TestReadTopology_ActualRigsQueryFails verifies that a failure in the
// actual_rigs group leaves snap.Actual.Rigs empty but other groups succeed.
func TestReadTopology_ActualRigsQueryFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	dbErr := errors.New("connection reset")

	// desired_rigs — succeeds with one row.
	mock.ExpectQuery(`SELECT name, enabled FROM desired_rigs`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "enabled"}).
			AddRow("rig-x", true))

	// desired_agent_config — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, role, enabled, max_polecats`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role", "enabled", "max_polecats"}))

	// desired_custom_roles — succeeds (empty).
	mock.ExpectQuery(`SELECT name, max_instances`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "max_instances"}))

	// desired_rig_custom_roles — succeeds (empty).
	mock.ExpectQuery(`SELECT cr\.name, rcr\.rig_name, cr\.max_instances`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "rig_name", "max_instances"}))

	// desired_formulas — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, name FROM desired_formulas`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "name"}))

	// actual_rigs — FAILS.
	mock.ExpectQuery(`SELECT name, enabled, status, last_seen FROM actual_rigs`).
		WillReturnError(dbErr)

	// actual_agent_config — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, role, status, last_seen FROM actual_agent_config`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role", "status", "last_seen"}))

	// actual_worktrees — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, status, last_seen FROM actual_worktrees`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "status", "last_seen"}))

	// actual_custom_roles — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, role_name, instance_index, status, last_seen`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role_name", "instance_index", "status", "last_seen"}))

	snap, snapErr := observer.ReadTopology(context.Background(), db)

	// Error must be returned and must mention actual_rigs.
	if snapErr == nil {
		t.Fatal("ReadTopology: expected error, got nil")
	}
	if !errors.Is(snapErr, dbErr) {
		t.Errorf("error chain: got %v, want to wrap %v", snapErr, dbErr)
	}

	// Desired rigs should still be populated (error isolation).
	if len(snap.Desired.Rigs) != 1 || snap.Desired.Rigs[0].Name != "rig-x" {
		t.Errorf("desired rigs: got %v, want [{rig-x ...}]", snap.Desired.Rigs)
	}

	// Actual rigs should be empty (the failing group).
	if len(snap.Actual.Rigs) != 0 {
		t.Errorf("actual rigs: got %v, want empty slice", snap.Actual.Rigs)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}

// TestReadTopology_DesiredRigsQueryFails verifies that a failure in the
// desired_rigs group leaves snap.Desired.Rigs empty but other groups succeed.
func TestReadTopology_DesiredRigsQueryFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	dbErr := errors.New("table not found")

	// desired_rigs — FAILS.
	mock.ExpectQuery(`SELECT name, enabled FROM desired_rigs`).
		WillReturnError(dbErr)

	// desired_custom_roles — succeeds (empty).
	mock.ExpectQuery(`SELECT name, max_instances`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "max_instances"}))

	// desired_rig_custom_roles — succeeds (empty).
	mock.ExpectQuery(`SELECT cr\.name, rcr\.rig_name, cr\.max_instances`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "rig_name", "max_instances"}))

	// desired_formulas — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, name FROM desired_formulas`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "name"}))

	// actual_rigs — succeeds with one row.
	now := time.Now()
	mock.ExpectQuery(`SELECT name, enabled, status, last_seen FROM actual_rigs`).
		WillReturnRows(sqlmock.NewRows([]string{"name", "enabled", "status", "last_seen"}).
			AddRow("rig-x", true, "running", now))

	// actual_agent_config — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, role, status, last_seen FROM actual_agent_config`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role", "status", "last_seen"}))

	// actual_worktrees — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, status, last_seen FROM actual_worktrees`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "status", "last_seen"}))

	// actual_custom_roles — succeeds (empty).
	mock.ExpectQuery(`SELECT rig_name, role_name, instance_index, status, last_seen`).
		WillReturnRows(sqlmock.NewRows([]string{"rig_name", "role_name", "instance_index", "status", "last_seen"}))

	snap, snapErr := observer.ReadTopology(context.Background(), db)

	if snapErr == nil {
		t.Fatal("ReadTopology: expected error, got nil")
	}
	if !errors.Is(snapErr, dbErr) {
		t.Errorf("error chain: got %v, want to wrap %v", snapErr, dbErr)
	}

	// desired_rigs is the group that failed.
	if len(snap.Desired.Rigs) != 0 {
		t.Errorf("desired rigs should be empty after query failure, got %v", snap.Desired.Rigs)
	}

	// Actual rigs from the succeeding group should still be populated.
	if len(snap.Actual.Rigs) != 1 || snap.Actual.Rigs[0].Name != "rig-x" {
		t.Errorf("actual rigs: got %v, want [{rig-x ...}]", snap.Actual.Rigs)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %v", err)
	}
}
