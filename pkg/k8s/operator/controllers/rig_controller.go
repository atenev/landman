package controllers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

const (
	rigDrainFinalizer = "gastown.io/rig-drain"

	statusSyncIntervalRig = 30 * time.Second

	// drainPollInterval is how often the controller re-checks actual_rigs.status
	// while waiting for a rig to fully drain.
	drainPollInterval = 10 * time.Second
)

// RigReconciler reconciles Rig CRs.
//
// Responsibilities:
//   - Write desired_rigs, desired_agent_config, desired_formulas, and
//     desired_rig_custom_roles to Dolt in a single atomic transaction (ADR-0003).
//   - Apply defaults from the parent GasTown.
//   - Handle deletion via the gastown.io/rig-drain finalizer.
//   - Sync actual_rigs → Rig status every 30s.
type RigReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	StatusSyncInterval time.Duration
	// ConnectDolt overrides the Dolt connection factory for testing.
	// When nil, openDoltConnectionFromSpec is used.
	ConnectDolt DoltConnector
}

// +kubebuilder:rbac:groups=gastown.tenev.io,resources=rigs,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=rigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=rigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconcile loop for Rig.
func (r *RigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("rig", req.NamespacedName)

	// 1. Fetch the Rig CR.
	var rig gasv1alpha1.Rig
	if err := r.Get(ctx, req.NamespacedName, &rig); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get rig: %w", err)
	}

	// 2. Handle deletion: run drain finalizer before the object is removed.
	if !rig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &rig)
	}

	// 3. Ensure the drain finalizer is registered.
	if !controllerutil.ContainsFinalizer(&rig, rigDrainFinalizer) {
		controllerutil.AddFinalizer(&rig, rigDrainFinalizer)
		if err := r.Update(ctx, &rig); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Resolve parent GasTown → DoltInstance readiness gate.
	var gt gasv1alpha1.GasTown
	if err := r.Get(ctx, client.ObjectKey{Name: rig.Spec.TownRef}, &gt); err != nil {
		logger.Info("parent gastown not found, requeuing", "townRef", rig.Spec.TownRef)
		r.setCondition(&rig, "DesiredTopologyInSync", metav1.ConditionFalse,
			"GasTownNotFound", fmt.Sprintf("gastown %q not found", rig.Spec.TownRef))
		_ = r.Status().Update(ctx, &rig)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	connectDolt := r.ConnectDolt
	if connectDolt == nil {
		connectDolt = openDoltConnectionFromSpec
	}
	dolt, err := connectDolt(ctx, r.Client, gt.Spec.DoltRef)
	if err != nil {
		logger.Info("dolt not ready, requeuing", "reason", err.Error())
		r.setCondition(&rig, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltNotReady", err.Error())
		_ = r.Status().Update(ctx, &rig)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	defer dolt.Close()

	// 5. Apply defaults from parent GasTown (defensive re-application; mutating
	// webhook is the primary enforcement point).
	resolved := resolveRigDefaults(&rig, &gt)

	// 6. Write desired topology to Dolt.
	doltCommit, err := r.syncToDolt(ctx, dolt, &rig, resolved)
	if err != nil {
		logger.Error(err, "failed to sync to dolt")
		r.setCondition(&rig, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltWriteFailed", err.Error())
		_ = r.Status().Update(ctx, &rig)
		return ctrl.Result{}, err
	}

	// 7. Update status.
	rig.Status.DoltCommit = doltCommit
	rig.Status.ObservedGeneration = rig.Generation
	r.setCondition(&rig, "DesiredTopologyInSync", metav1.ConditionTrue, "Synced",
		"desired_rigs written successfully")
	if err := r.Status().Update(ctx, &rig); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	logger.Info("reconciled", "doltCommit", doltCommit)
	return ctrl.Result{}, nil
}

// resolvedRigDefaults carries the effective values after applying GasTown defaults.
type resolvedRigDefaults struct {
	MayorModel   string
	PolecatModel string
	MaxPolecats  int32
}

// resolveRigDefaults computes effective agent config by merging rig overrides with
// GasTown defaults.
func resolveRigDefaults(rig *gasv1alpha1.Rig, gt *gasv1alpha1.GasTown) resolvedRigDefaults {
	mayorModel := rig.Spec.Agents.MayorModel
	if mayorModel == "" {
		mayorModel = gt.Spec.Defaults.MayorModel
	}
	if mayorModel == "" {
		mayorModel = "claude-opus-4-6"
	}

	polecatModel := rig.Spec.Agents.PolecatModel
	if polecatModel == "" {
		polecatModel = gt.Spec.Defaults.PolecatModel
	}
	if polecatModel == "" {
		polecatModel = "claude-sonnet-4-6"
	}

	maxPolecats := rig.Spec.Agents.MaxPolecats
	if maxPolecats == 0 {
		maxPolecats = gt.Spec.Defaults.MaxPolecats
	}
	if maxPolecats == 0 {
		maxPolecats = 20
	}

	return resolvedRigDefaults{
		MayorModel:   mayorModel,
		PolecatModel: polecatModel,
		MaxPolecats:  maxPolecats,
	}
}

// syncToDolt writes all desired topology rows for a Rig in a single atomic
// transaction (ADR-0003: versions first). Returns the Dolt commit hash.
func (r *RigReconciler) syncToDolt(
	ctx context.Context,
	dolt *doltClient,
	rig *gasv1alpha1.Rig,
	resolved resolvedRigDefaults,
) (string, error) {
	// Pre-flight: ensure no CLI write is in progress (dgt-lc3).
	if err := checkTopologyLock(ctx, dolt.db); err != nil {
		return "", err
	}

	tx, err := dolt.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// ADR-0003: upsert versions first.
	if err := upsertTopologyVersions(ctx, tx, []tableVersion{
		{Table: "desired_rigs", Version: 1},
		{Table: "desired_agent_config", Version: 1},
		{Table: "desired_formulas", Version: 1},
		{Table: "desired_rig_custom_roles", Version: 1},
	}); err != nil {
		return "", err
	}

	// Claim advisory write lock (dgt-lc3).
	if err := upsertTopologyLock(ctx, tx); err != nil {
		return "", err
	}

	// a. UPSERT desired_rigs
	const upsertRig = `
INSERT INTO desired_rigs (name, repo, branch, enabled)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  repo    = VALUES(repo),
  branch  = VALUES(branch),
  enabled = VALUES(enabled)`

	if _, err := tx.ExecContext(ctx, upsertRig,
		rig.Name,
		rig.Spec.Repo,
		rig.Spec.Branch,
		rig.Spec.Enabled,
	); err != nil {
		return "", fmt.Errorf("upsert desired_rigs: %w", err)
	}

	// b. UPSERT desired_agent_config
	mayorClaudeMdPath := fmt.Sprintf("/gt/rigs/%s/CLAUDE.md", rig.Name)

	const upsertAgentConfig = `
INSERT INTO desired_agent_config
  (rig_name, mayor_enabled, witness_enabled, refinery_enabled, deacon_enabled,
   mayor_model, polecat_model, max_polecats, mayor_claude_md_path)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  mayor_enabled      = VALUES(mayor_enabled),
  witness_enabled    = VALUES(witness_enabled),
  refinery_enabled   = VALUES(refinery_enabled),
  deacon_enabled     = VALUES(deacon_enabled),
  mayor_model        = VALUES(mayor_model),
  polecat_model      = VALUES(polecat_model),
  max_polecats       = VALUES(max_polecats),
  mayor_claude_md_path = VALUES(mayor_claude_md_path)`

	if _, err := tx.ExecContext(ctx, upsertAgentConfig,
		rig.Name,
		rig.Spec.Agents.Mayor,
		rig.Spec.Agents.Witness,
		rig.Spec.Agents.Refinery,
		rig.Spec.Agents.Deacon,
		resolved.MayorModel,
		resolved.PolecatModel,
		resolved.MaxPolecats,
		mayorClaudeMdPath,
	); err != nil {
		return "", fmt.Errorf("upsert desired_agent_config: %w", err)
	}

	// c. Replace desired_formulas for this rig.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM desired_formulas WHERE rig_name = ?`, rig.Name,
	); err != nil {
		return "", fmt.Errorf("delete desired_formulas: %w", err)
	}

	if len(rig.Spec.Formulas) > 0 {
		const insertFormula = `
INSERT INTO desired_formulas (rig_name, formula_name, schedule)
VALUES (?, ?, ?)`
		for _, f := range rig.Spec.Formulas {
			if _, err := tx.ExecContext(ctx, insertFormula, rig.Name, f.Name, f.Schedule); err != nil {
				return "", fmt.Errorf("insert desired_formulas %q: %w", f.Name, err)
			}
		}
	}

	// d. Replace desired_rig_custom_roles for this rig.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM desired_rig_custom_roles WHERE rig_name = ?`, rig.Name,
	); err != nil {
		return "", fmt.Errorf("delete desired_rig_custom_roles: %w", err)
	}

	if len(rig.Spec.Roles) > 0 {
		const insertRigRole = `
INSERT INTO desired_rig_custom_roles (rig_name, role_name, enabled)
VALUES (?, ?, true)`
		for _, roleName := range rig.Spec.Roles {
			if _, err := tx.ExecContext(ctx, insertRigRole, rig.Name, roleName); err != nil {
				return "", fmt.Errorf("insert desired_rig_custom_roles %q: %w", roleName, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	var commitHash string
	row := dolt.db.QueryRowContext(ctx, `SELECT dolt_hashof('HEAD')`)
	if err := row.Scan(&commitHash); err != nil {
		commitHash = ""
	}
	return commitHash, nil
}

// handleDeletion implements the drain finalizer for Rig deletion:
//  1. Set desired_rigs.enabled=false (drain signal to Surveyor).
//  2. Poll actual_rigs.status until 'stopped'.
//  3. Remove the finalizer.
func (r *RigReconciler) handleDeletion(ctx context.Context, rig *gasv1alpha1.Rig) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(rig, rigDrainFinalizer) {
		return ctrl.Result{}, nil
	}

	var gt gasv1alpha1.GasTown
	if err := r.Get(ctx, client.ObjectKey{Name: rig.Spec.TownRef}, &gt); err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second},
			fmt.Errorf("get gastown during drain: %w", err)
	}

	connectDolt := r.ConnectDolt
	if connectDolt == nil {
		connectDolt = openDoltConnectionFromSpec
	}
	dolt, err := connectDolt(ctx, r.Client, gt.Spec.DoltRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second},
			fmt.Errorf("dolt not ready during drain: %w", err)
	}
	defer dolt.Close()

	// Write enabled=false first (idempotent).
	if err := r.setRigDisabled(ctx, dolt, rig.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("drain: disable rig: %w", err)
	}

	// Check if rig is stopped in actual_rigs.
	stopped, err := r.isRigStopped(ctx, dolt, rig.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("drain: check actual_rigs: %w", err)
	}
	if !stopped {
		return ctrl.Result{RequeueAfter: drainPollInterval}, nil
	}

	// Rig is stopped — remove finalizer.
	controllerutil.RemoveFinalizer(rig, rigDrainFinalizer)
	if err := r.Update(ctx, rig); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// setRigDisabled writes desired_rigs SET enabled=false for the named rig.
func (r *RigReconciler) setRigDisabled(ctx context.Context, dolt *doltClient, rigName string) error {
	// Pre-flight: ensure no CLI write is in progress (dgt-lc3).
	if err := checkTopologyLock(ctx, dolt.db); err != nil {
		return err
	}

	tx, err := dolt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertTopologyVersions(ctx, tx, []tableVersion{
		{Table: "desired_rigs", Version: 1},
	}); err != nil {
		return err
	}

	// Claim advisory write lock (dgt-lc3).
	if err := upsertTopologyLock(ctx, tx); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE desired_rigs SET enabled = false WHERE name = ?`, rigName,
	); err != nil {
		return fmt.Errorf("disable rig in desired_rigs: %w", err)
	}
	return tx.Commit()
}

// isRigStopped returns true when actual_rigs.status = 'stopped' for this rig.
func (r *RigReconciler) isRigStopped(ctx context.Context, dolt *doltClient, rigName string) (bool, error) {
	const query = `SELECT status FROM actual_rigs WHERE name = ? LIMIT 1`
	var status string
	err := dolt.db.QueryRowContext(ctx, query, rigName).Scan(&status)
	if err == sql.ErrNoRows {
		// Not yet in actual_rigs — treat as stopped (rig was never active).
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("query actual_rigs: %w", err)
	}
	return status == "stopped", nil
}

// StartStatusSync launches a goroutine that periodically polls actual_rigs
// and patches Rig status fields. The goroutine exits when ctx is cancelled.
func (r *RigReconciler) StartStatusSync(ctx context.Context) {
	interval := r.StatusSyncInterval
	if interval == 0 {
		interval = statusSyncIntervalRig
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		logger := log.FromContext(ctx).WithName("rig-status-sync")
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.runStatusSync(ctx); err != nil {
					logger.Error(err, "status sync failed")
				}
			}
		}
	}()
}

// runStatusSync lists all Rigs and, for each, reads actual_rigs from Dolt and
// patches status fields.
func (r *RigReconciler) runStatusSync(ctx context.Context) error {
	var rigList gasv1alpha1.RigList
	if err := r.List(ctx, &rigList); err != nil {
		return fmt.Errorf("list rigs: %w", err)
	}

	type connKey struct{ doltName, doltNamespace string }
	conns := map[connKey]*doltClient{}
	defer func() {
		for _, d := range conns {
			d.Close()
		}
	}()

	for i := range rigList.Items {
		rig := &rigList.Items[i]

		var gt gasv1alpha1.GasTown
		if err := r.Get(ctx, client.ObjectKey{Name: rig.Spec.TownRef}, &gt); err != nil {
			continue
		}

		key := connKey{gt.Spec.DoltRef.Name, gt.Spec.DoltRef.Namespace}
		dolt, ok := conns[key]
		if !ok {
			var err error
			dolt, err = openDoltConnectionFromSpec(ctx, r.Client, gt.Spec.DoltRef)
			if err != nil {
				continue
			}
			conns[key] = dolt
		}

		if err := r.patchStatusFromActual(ctx, dolt, rig); err != nil {
			log.FromContext(ctx).Error(err, "patch rig status", "name", rig.Name)
		}
	}
	return nil
}

// patchStatusFromActual queries actual_rigs and patches Rig status.
func (r *RigReconciler) patchStatusFromActual(
	ctx context.Context,
	dolt *doltClient,
	rig *gasv1alpha1.Rig,
) error {
	const rigQuery = `
SELECT status
FROM actual_rigs
WHERE name = ?
LIMIT 1`

	var rigStatus string
	err := dolt.db.QueryRowContext(ctx, rigQuery, rig.Name).Scan(&rigStatus)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("query actual_rigs: %w", err)
	}

	const polecatQuery = `
SELECT COUNT(*)
FROM actual_agent_config
WHERE rig_name = ? AND role = 'polecat' AND status = 'running'`

	var runningPolecats int32
	if err := dolt.db.QueryRowContext(ctx, polecatQuery, rig.Name).Scan(&runningPolecats); err != nil {
		runningPolecats = 0
	}

	const convergedQuery = `
SELECT last_converged_at
FROM reconcile_log
WHERE rig_name = ?
ORDER BY last_converged_at DESC
LIMIT 1`

	var lastConvergedAt sql.NullTime
	_ = dolt.db.QueryRowContext(ctx, convergedQuery, rig.Name).Scan(&lastConvergedAt)

	base := rig.DeepCopy()
	rig.Status.RigHealth = rigStatusToHealth(rigStatus)
	rig.Status.RunningPolecats = runningPolecats
	if lastConvergedAt.Valid {
		t := metav1.NewTime(lastConvergedAt.Time)
		rig.Status.LastConvergedAt = &t
	}

	if rig.Status.RigHealth != base.Status.RigHealth ||
		rig.Status.RunningPolecats != base.Status.RunningPolecats ||
		!timeEqual(rig.Status.LastConvergedAt, base.Status.LastConvergedAt) {
		if err := r.Status().Update(ctx, rig); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
	}
	return nil
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
func (r *RigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gasv1alpha1.Rig{}).
		Complete(r)
}

// setCondition updates or appends a metav1.Condition on the Rig status.
func (r *RigReconciler) setCondition(
	rig *gasv1alpha1.Rig,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&rig.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: rig.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	})
}

// rigStatusToHealth maps actual_rigs.status values to the RigHealth string.
func rigStatusToHealth(status string) string {
	switch status {
	case "running":
		return "healthy"
	case "degraded":
		return "degraded"
	default:
		return "unknown"
	}
}
