// Package controllers — unit tests for patchStatusFromActual (dgt-r108).
//
// These tests verify the ConvergenceScore patching logic introduced in
// dgt-w9m without requiring a real Dolt endpoint or envtest binary.
// A routing fake SQL driver dispatches queries by substring to per-test row
// sets, enabling deterministic convergence scenarios.
package controllers

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// ─── routing fake SQL driver ──────────────────────────────────────────────────
//
// Registered once as "fake-dolt-status". Each *sql.DB is identified by a
// unique DSN string that maps to a statusFakeRoutes config. Queries are
// dispatched to the first route whose Contains substring is found in the SQL.

var (
	statusFakeDriverOnce sync.Once
	statusFakeDSNMu      sync.Mutex
	statusFakeDSNMap     = map[string]*statusFakeRoutes{}
	statusFakeDSNCtr     atomic.Int64
)

// statusFakeRoute maps a SQL substring to a set of result rows.
type statusFakeRoute struct {
	// Contains is the substring that must appear in the SQL to match.
	Contains string
	// Cols is the column name list returned by Columns().
	Cols []string
	// Rows is the data rows returned; nil means empty result set.
	Rows [][]driver.Value
	// Err, when non-nil, causes Query to return an error instead of rows.
	Err error
}

// statusFakeRoutes holds an ordered list of routes for one fake DB.
type statusFakeRoutes struct {
	routes []statusFakeRoute
}

func registerStatusFakeDriver() {
	statusFakeDriverOnce.Do(func() {
		sql.Register("fake-dolt-status", &statusFakeDriver{})
	})
}

// newStatusFakeDB creates a *sql.DB backed by the routing fake driver.
func newStatusFakeDB(t *testing.T, routes *statusFakeRoutes) *sql.DB {
	t.Helper()
	registerStatusFakeDriver()

	dsn := fmt.Sprintf("status-fake-%d", statusFakeDSNCtr.Add(1))
	statusFakeDSNMu.Lock()
	statusFakeDSNMap[dsn] = routes
	statusFakeDSNMu.Unlock()
	t.Cleanup(func() {
		statusFakeDSNMu.Lock()
		delete(statusFakeDSNMap, dsn)
		statusFakeDSNMu.Unlock()
	})

	db, err := sql.Open("fake-dolt-status", dsn)
	if err != nil {
		t.Fatalf("sql.Open fake-dolt-status: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// ─── driver.Driver ────────────────────────────────────────────────────────────

type statusFakeDriver struct{}

func (d *statusFakeDriver) Open(dsn string) (driver.Conn, error) {
	statusFakeDSNMu.Lock()
	routes, ok := statusFakeDSNMap[dsn]
	statusFakeDSNMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake-dolt-status: no config for DSN %q", dsn)
	}
	return &statusFakeConn{routes: routes}, nil
}

// ─── driver.Conn ──────────────────────────────────────────────────────────────

type statusFakeConn struct{ routes *statusFakeRoutes }

func (c *statusFakeConn) Prepare(query string) (driver.Stmt, error) {
	return &statusFakeStmt{routes: c.routes, query: query}, nil
}
func (c *statusFakeConn) Close() error                        { return nil }
func (c *statusFakeConn) Begin() (driver.Tx, error)           { return &statusFakeTx{}, nil }

type statusFakeTx struct{}

func (t *statusFakeTx) Commit() error   { return nil }
func (t *statusFakeTx) Rollback() error { return nil }

// ─── driver.Stmt ──────────────────────────────────────────────────────────────

type statusFakeStmt struct {
	routes *statusFakeRoutes
	query  string
}

func (s *statusFakeStmt) Close() error  { return nil }
func (s *statusFakeStmt) NumInput() int { return -1 }
func (s *statusFakeStmt) Exec(_ []driver.Value) (driver.Result, error) {
	return statusFakeResult{}, nil
}
func (s *statusFakeStmt) Query(_ []driver.Value) (driver.Rows, error) {
	for _, route := range s.routes.routes {
		if strings.Contains(s.query, route.Contains) {
			if route.Err != nil {
				return nil, route.Err
			}
			return &statusFakeRows{cols: route.Cols, rows: route.Rows}, nil
		}
	}
	// No matching route → return empty rows (no columns needed for zero-row result).
	return &statusFakeRows{cols: nil, rows: nil}, nil
}

type statusFakeResult struct{}

func (r statusFakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r statusFakeResult) RowsAffected() (int64, error) { return 0, nil }

// ─── driver.Rows ──────────────────────────────────────────────────────────────

type statusFakeRows struct {
	cols []string
	rows [][]driver.Value
	pos  int
}

func (r *statusFakeRows) Columns() []string { return r.cols }
func (r *statusFakeRows) Close() error      { return nil }
func (r *statusFakeRows) Next(dest []driver.Value) error {
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

// ─── test helpers ─────────────────────────────────────────────────────────────

// newGasTownScheme builds a runtime.Scheme with GasTown types registered.
func newGasTownScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

// getConditionReason returns the Reason field for condType, or "" if not found.
func getConditionReason(conditions []metav1.Condition, condType string) string {
	for i := range conditions {
		if conditions[i].Type == condType {
			return conditions[i].Reason
		}
	}
	return ""
}

// getConditionStatus returns the Status field for condType, or "" if not found.
func getConditionStatus(conditions []metav1.Condition, condType string) metav1.ConditionStatus {
	for i := range conditions {
		if conditions[i].Type == condType {
			return conditions[i].Status
		}
	}
	return ""
}

// convergedRoutes builds routes for a single fully-converged rig named rigName.
func convergedRoutes(t *testing.T, rigName string, now time.Time) *statusFakeRoutes {
	t.Helper()
	lastSeen := now.Add(-10 * time.Second) // within StaleTTL (60s)
	lastReconcile := now.Add(-5 * time.Second)
	return &statusFakeRoutes{routes: []statusFakeRoute{
		{
			Contains: "actual_town",
			Cols:     []string{"last_reconcile_at"},
			Rows:     [][]driver.Value{{lastReconcile}},
		},
		{
			Contains: "desired_rigs",
			Cols:     []string{"name", "enabled", "max_polecats"},
			Rows:     [][]driver.Value{{rigName, true, int64(5)}},
		},
		{
			Contains: "actual_rigs",
			Cols:     []string{"name", "enabled", "status", "last_seen"},
			Rows:     [][]driver.Value{{rigName, true, "running", lastSeen}},
		},
		{
			Contains: "actual_agent_config",
			Cols:     []string{"rig_name", "role", "status", "last_seen"},
			Rows:     [][]driver.Value{{rigName, "mayor", "running", lastSeen}},
		},
	}}
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestPatchStatusFromActual_ConvergenceScore_Computed verifies that a
// single fully-converged rig produces ConvergenceScore == 1.0 and sets
// LastConvergenceAt to a non-nil time.
func TestPatchStatusFromActual_ConvergenceScore_Computed(t *testing.T) {
	now := time.Now()
	routes := convergedRoutes(t, "rig-a", now)

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).
		WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	dolt := &doltClient{db: db}

	if err := r.patchStatusFromActual(context.Background(), dolt, gt); err != nil {
		t.Fatalf("patchStatusFromActual: %v", err)
	}

	if got, want := gt.Status.ConvergenceScore, 1.0; got != want {
		t.Errorf("ConvergenceScore = %.3f, want %.3f", got, want)
	}
	if gt.Status.LastConvergenceAt == nil {
		t.Error("LastConvergenceAt is nil, want non-nil")
	}

	// Verify the fake client's stored copy was updated.
	var stored gasv1alpha1.GasTown
	if err := c.Get(context.Background(), types.NamespacedName{Name: "my-town"}, &stored); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status.ConvergenceScore != 1.0 {
		t.Errorf("stored ConvergenceScore = %.3f, want 1.0", stored.Status.ConvergenceScore)
	}
}

// TestPatchStatusFromActual_FleetConverged_True_When_Score1 verifies that the
// FleetConverged condition is True when ConvergenceScore == 1.0.
func TestPatchStatusFromActual_FleetConverged_True_When_Score1(t *testing.T) {
	now := time.Now()
	routes := convergedRoutes(t, "rig-a", now)

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).
		WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual: %v", err)
	}

	if got := getConditionStatus(gt.Status.Conditions, gasv1alpha1.ConditionFleetConverged); got != metav1.ConditionTrue {
		t.Errorf("FleetConverged = %q, want True", got)
	}
	if got := getConditionStatus(gt.Status.Conditions, gasv1alpha1.ConditionActualTopologyAvailable); got != metav1.ConditionTrue {
		t.Errorf("ActualTopologyAvailable = %q, want True", got)
	}
}

// TestPatchStatusFromActual_FleetConverged_False_PartialConvergence verifies
// that when score < 1.0 the FleetConverged condition is False with reason
// PartialConvergence.
func TestPatchStatusFromActual_FleetConverged_False_PartialConvergence(t *testing.T) {
	now := time.Now()
	lastReconcile := now.Add(-5 * time.Second)
	lastSeen := now.Add(-10 * time.Second)

	// Two desired rigs, only rig-a is actually converging.
	routes := &statusFakeRoutes{routes: []statusFakeRoute{
		{
			Contains: "actual_town",
			Cols:     []string{"last_reconcile_at"},
			Rows:     [][]driver.Value{{lastReconcile}},
		},
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
			Rows:     [][]driver.Value{{"rig-a", true, "running", lastSeen}},
		},
		{
			Contains: "actual_agent_config",
			Cols:     []string{"rig_name", "role", "status", "last_seen"},
			Rows:     [][]driver.Value{{"rig-a", "mayor", "running", lastSeen}},
		},
	}}

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{ObjectMeta: metav1.ObjectMeta{Name: "my-town"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual: %v", err)
	}

	if score := gt.Status.ConvergenceScore; score >= 1.0 {
		t.Errorf("ConvergenceScore = %.3f, want < 1.0", score)
	}
	if got := getConditionStatus(gt.Status.Conditions, gasv1alpha1.ConditionFleetConverged); got != metav1.ConditionFalse {
		t.Errorf("FleetConverged = %q, want False", got)
	}
	if got := getConditionReason(gt.Status.Conditions, gasv1alpha1.ConditionFleetConverged); got != "PartialConvergence" {
		t.Errorf("FleetConverged reason = %q, want PartialConvergence", got)
	}
}

// TestPatchStatusFromActual_NonConverged_CappedAt20 verifies that when more
// than 20 resources are non-converged, the list is truncated to 20 entries
// and the last entry reads "... and N more".
func TestPatchStatusFromActual_NonConverged_CappedAt20(t *testing.T) {
	now := time.Now()
	lastReconcile := now.Add(-5 * time.Second)

	// 13 desired rigs with WitnessEnabled=true (via desired_agent_config),
	// no actual data. Each rig contributes 2 NonConverged entries (rig + pool),
	// yielding 26 total → capped to 20 with "... and 7 more".
	desiredRows := make([][]driver.Value, 13)
	agentRows := make([][]driver.Value, 13)
	for i := 0; i < 13; i++ {
		name := fmt.Sprintf("rig-%d", i)
		desiredRows[i] = []driver.Value{name, true, int64(5)}
		agentRows[i] = []driver.Value{name, "witness"}
	}

	routes := &statusFakeRoutes{routes: []statusFakeRoute{
		{
			Contains: "actual_town",
			Cols:     []string{"last_reconcile_at"},
			Rows:     [][]driver.Value{{lastReconcile}},
		},
		{
			Contains: "desired_rigs",
			Cols:     []string{"name", "enabled", "max_polecats"},
			Rows:     desiredRows,
		},
		{
			Contains: "desired_agent_config",
			Cols:     []string{"rig_name", "role"},
			Rows:     agentRows,
		},
		// No actual_rigs, actual_agent_config, etc. → all rigs non-converging.
	}}

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{ObjectMeta: metav1.ObjectMeta{Name: "my-town"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual: %v", err)
	}

	nc := gt.Status.NonConverged
	if len(nc) != 20 {
		t.Errorf("len(NonConverged) = %d, want 20", len(nc))
	}
	if len(nc) > 0 {
		last := nc[len(nc)-1]
		if !strings.HasPrefix(last, "... and ") || !strings.HasSuffix(last, " more") {
			t.Errorf("last NonConverged entry = %q, want '... and N more'", last)
		}
	}
}

// TestPatchStatusFromActual_LastConvergenceAt_NotNil verifies that
// LastConvergenceAt is set to a non-nil time when scores are computed.
func TestPatchStatusFromActual_LastConvergenceAt_NotNil(t *testing.T) {
	now := time.Now()
	routes := convergedRoutes(t, "rig-a", now)

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{ObjectMeta: metav1.ObjectMeta{Name: "my-town"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual: %v", err)
	}

	if gt.Status.LastConvergenceAt == nil {
		t.Error("LastConvergenceAt is nil after successful score computation")
	}
}

// TestPatchStatusFromActual_ActualTopologyAvailable_False_SurveyorNotStarted
// verifies that when readTopologyForStatus fails (desired_rigs query error),
// the ActualTopologyAvailable condition is set to False with reason
// SurveyorNotStarted.
func TestPatchStatusFromActual_ActualTopologyAvailable_False_SurveyorNotStarted(t *testing.T) {
	now := time.Now()
	lastReconcile := now.Add(-5 * time.Second)
	topologyErr := fmt.Errorf("simulated desired_rigs SQL failure")

	routes := &statusFakeRoutes{routes: []statusFakeRoute{
		{
			Contains: "actual_town",
			Cols:     []string{"last_reconcile_at"},
			Rows:     [][]driver.Value{{lastReconcile}},
		},
		{
			Contains: "desired_rigs",
			Err:      topologyErr,
		},
	}}

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{ObjectMeta: metav1.ObjectMeta{Name: "my-town"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual returned unexpected error: %v", err)
	}

	if got := getConditionStatus(gt.Status.Conditions, gasv1alpha1.ConditionActualTopologyAvailable); got != metav1.ConditionFalse {
		t.Errorf("ActualTopologyAvailable = %q, want False", got)
	}
	if got := getConditionReason(gt.Status.Conditions, gasv1alpha1.ConditionActualTopologyAvailable); got != "SurveyorNotStarted" {
		t.Errorf("ActualTopologyAvailable reason = %q, want SurveyorNotStarted", got)
	}
}

// TestPatchStatusFromActual_ScoreFailureNonFatal verifies that a topology read
// error does not prevent the status update from proceeding — LastReconcileAt is
// still set when actual_town row is present.
func TestPatchStatusFromActual_ScoreFailureNonFatal(t *testing.T) {
	now := time.Now()
	lastReconcile := now.Add(-5 * time.Second)

	routes := &statusFakeRoutes{routes: []statusFakeRoute{
		{
			Contains: "actual_town",
			Cols:     []string{"last_reconcile_at"},
			Rows:     [][]driver.Value{{lastReconcile}},
		},
		{
			// desired_rigs fails → readTopologyForStatus errors → non-fatal.
			Contains: "desired_rigs",
			Err:      fmt.Errorf("topology unavailable"),
		},
	}}

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{ObjectMeta: metav1.ObjectMeta{Name: "my-town"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	// Must return nil — score failure is non-fatal.
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual returned error, want nil: %v", err)
	}

	// Status update still proceeded: LastReconcileAt must be set.
	if gt.Status.LastReconcileAt == nil {
		t.Error("LastReconcileAt is nil after score failure; status update did not proceed")
	}

	// Verify stored copy was updated too.
	var stored gasv1alpha1.GasTown
	if err := c.Get(context.Background(), types.NamespacedName{Name: "my-town"}, &stored); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status.LastReconcileAt == nil {
		t.Error("stored LastReconcileAt is nil, want non-nil")
	}
}

// TestPatchStatusFromActual_NoActualTownRow_ReturnsNil verifies that when the
// actual_town table has no row for this GasTown, patchStatusFromActual returns
// nil without modifying status.
func TestPatchStatusFromActual_NoActualTownRow_ReturnsNil(t *testing.T) {
	routes := &statusFakeRoutes{routes: []statusFakeRoute{
		// actual_town returns no rows (sql.ErrNoRows path).
		{
			Contains: "actual_town",
			Cols:     []string{"last_reconcile_at"},
			Rows:     nil,
		},
	}}

	db := newStatusFakeDB(t, routes)
	s := newGasTownScheme(t)
	gt := &gasv1alpha1.GasTown{ObjectMeta: metav1.ObjectMeta{Name: "my-town"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt).WithStatusSubresource(gt).Build()

	r := &GasTownReconciler{Client: c, Scheme: s}
	if err := r.patchStatusFromActual(context.Background(), &doltClient{db: db}, gt); err != nil {
		t.Fatalf("patchStatusFromActual: %v", err)
	}

	// Status must remain unchanged (no actual_town row → early return).
	if gt.Status.LastReconcileAt != nil {
		t.Error("LastReconcileAt was set; expected it to remain nil")
	}
	if len(gt.Status.Conditions) != 0 {
		t.Errorf("Conditions set unexpectedly: %+v", gt.Status.Conditions)
	}
}
