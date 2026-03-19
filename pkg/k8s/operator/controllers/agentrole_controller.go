package controllers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

const (
	agentRoleCleanupFinalizer = "gastown.io/role-cleanup"

	// statusSyncIntervalAgentRole is how often the status-sync goroutine polls
	// actual_custom_roles and patches AgentRole status.
	statusSyncIntervalAgentRole = 30 * time.Second
)

// reservedRoleNames is the canonical list of built-in Gas Town agent roles.
// AgentRole CRs may not use any of these names.
var reservedRoleNames = map[string]bool{
	"mayor":    true,
	"polecat":  true,
	"witness":  true,
	"refinery": true,
	"deacon":   true,
	"dog":      true,
	"crew":     true,
}

// AgentRoleReconciler reconciles AgentRole CRs.
//
// Responsibilities:
//   - Write desired_custom_roles rows to Dolt (ADR-0003 compliant).
//   - Manage the gastown.io/role-cleanup finalizer for cascade deletion.
//   - Sync actual_custom_roles → AgentRole status every 30s.
type AgentRoleReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	StatusSyncInterval time.Duration
}

// +kubebuilder:rbac:groups=gastown.tenev.io,resources=agentroles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=agentroles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=agentroles/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconcile loop for AgentRole.
func (r *AgentRoleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("agentrole", req.NamespacedName)

	// 1. Fetch the AgentRole CR.
	var ar gasv1alpha1.AgentRole
	if err := r.Get(ctx, req.NamespacedName, &ar); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get agentrole: %w", err)
	}

	// 2. Handle deletion: run the cleanup finalizer before the object is removed.
	if !ar.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &ar)
	}

	// 3. Ensure the cleanup finalizer is registered.
	if !controllerutil.ContainsFinalizer(&ar, agentRoleCleanupFinalizer) {
		controllerutil.AddFinalizer(&ar, agentRoleCleanupFinalizer)
		if err := r.Update(ctx, &ar); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Defensive: validate name is not reserved (admission webhook is primary;
	// this check prevents bugs if the webhook is bypassed).
	if reservedRoleNames[ar.Name] {
		r.emitEvent(ctx, &ar, corev1.EventTypeWarning, "ReservedName",
			fmt.Sprintf("AgentRole name %q is reserved for a built-in role; skipping reconcile", ar.Name))
		r.setCondition(&ar, "DesiredTopologyInSync", metav1.ConditionFalse, "ReservedName",
			fmt.Sprintf("name %q is reserved", ar.Name))
		_ = r.Status().Update(ctx, &ar)
		return ctrl.Result{}, nil
	}

	// 5. Resolve parent GasTown → DoltInstance readiness gate.
	dolt, err := openDoltConnection(ctx, r.Client, ar.Spec.TownRef, ar.Namespace)
	if err != nil {
		logger.Info("dolt not ready, requeuing", "reason", err.Error())
		r.setCondition(&ar, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltNotReady", err.Error())
		_ = r.Status().Update(ctx, &ar)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	defer dolt.Close()

	// 6. Write desired_custom_roles to Dolt.
	doltCommit, err := r.syncToDolt(ctx, dolt, &ar)
	if err != nil {
		logger.Error(err, "failed to sync to dolt")
		r.setCondition(&ar, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltWriteFailed", err.Error())
		_ = r.Status().Update(ctx, &ar)
		return ctrl.Result{}, err
	}

	// 7. Update status.
	ar.Status.DoltCommit = doltCommit
	ar.Status.ObservedGeneration = ar.Generation
	r.setCondition(&ar, "DesiredTopologyInSync", metav1.ConditionTrue, "Synced",
		"desired_custom_roles written successfully")
	if err := r.Status().Update(ctx, &ar); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	logger.Info("reconciled", "doltCommit", doltCommit)
	return ctrl.Result{}, nil
}

// syncToDolt writes the AgentRole spec to desired_custom_roles within a
// versions-first transaction (ADR-0003). Returns the Dolt commit hash.
func (r *AgentRoleReconciler) syncToDolt(
	ctx context.Context,
	dolt *doltClient,
	ar *gasv1alpha1.AgentRole,
) (string, error) {
	tx, err := dolt.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// ADR-0003: upsert versions first.
	versions := []tableVersion{
		{Table: "desired_custom_roles", Version: 1},
		{Table: "desired_rig_custom_roles", Version: 1},
	}
	if err := upsertTopologyVersions(ctx, tx, versions); err != nil {
		return "", err
	}

	// Compute the claude_md_path: "/gt/roles/<name>/CLAUDE.md"
	claudeMdPath := fmt.Sprintf("/gt/roles/%s/CLAUDE.md", ar.Name)

	// Nullable fields.
	var triggerSchedule, triggerEvent sql.NullString
	if ar.Spec.Trigger.Schedule != "" {
		triggerSchedule = sql.NullString{String: ar.Spec.Trigger.Schedule, Valid: true}
	}
	if ar.Spec.Trigger.Event != "" {
		triggerEvent = sql.NullString{String: ar.Spec.Trigger.Event, Valid: true}
	}

	reportsTo := ar.Spec.Supervision.ReportsTo
	if reportsTo == "" {
		reportsTo = ar.Spec.Supervision.Parent
	}

	maxInstances := ar.Spec.Resources.MaxInstances
	if maxInstances == 0 {
		maxInstances = 1
	}

	const upsertRole = `
INSERT INTO desired_custom_roles
  (name, scope, claude_md_path, trigger_type, trigger_schedule,
   trigger_event, parent_role, reports_to, max_instances)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  scope            = VALUES(scope),
  claude_md_path   = VALUES(claude_md_path),
  trigger_type     = VALUES(trigger_type),
  trigger_schedule = VALUES(trigger_schedule),
  trigger_event    = VALUES(trigger_event),
  parent_role      = VALUES(parent_role),
  reports_to       = VALUES(reports_to),
  max_instances    = VALUES(max_instances)`

	if _, err := tx.ExecContext(ctx, upsertRole,
		ar.Name,
		ar.Spec.Scope,
		claudeMdPath,
		ar.Spec.Trigger.Type,
		triggerSchedule,
		triggerEvent,
		ar.Spec.Supervision.Parent,
		reportsTo,
		maxInstances,
	); err != nil {
		return "", fmt.Errorf("upsert desired_custom_roles: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	// Read back the Dolt commit hash.
	var commitHash string
	row := dolt.db.QueryRowContext(ctx, `SELECT dolt_hashof('HEAD')`)
	if err := row.Scan(&commitHash); err != nil {
		// Non-fatal: best-effort commit hash.
		commitHash = ""
	}
	return commitHash, nil
}

// handleDeletion runs the cleanup finalizer:
//  1. Cascades DELETE desired_rig_custom_roles rows that reference this role.
//  2. DELETEs the desired_custom_roles row.
//  3. Removes the finalizer so the object can be garbage-collected.
func (r *AgentRoleReconciler) handleDeletion(
	ctx context.Context,
	ar *gasv1alpha1.AgentRole,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(ar, agentRoleCleanupFinalizer) {
		return ctrl.Result{}, nil
	}

	// Try to open a Dolt connection; if Dolt is unavailable, requeue.
	dolt, err := openDoltConnection(ctx, r.Client, ar.Spec.TownRef, ar.Namespace)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second},
			fmt.Errorf("dolt not ready during deletion: %w", err)
	}
	defer dolt.Close()

	if err := r.deleteFromDolt(ctx, dolt, ar.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete from dolt: %w", err)
	}

	// Remove finalizer.
	controllerutil.RemoveFinalizer(ar, agentRoleCleanupFinalizer)
	if err := r.Update(ctx, ar); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteFromDolt removes the role and all its junction rows in one transaction.
func (r *AgentRoleReconciler) deleteFromDolt(
	ctx context.Context,
	dolt *doltClient,
	roleName string,
) error {
	tx, err := dolt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// ADR-0003: versions-first even on delete paths.
	if err := upsertTopologyVersions(ctx, tx, []tableVersion{
		{Table: "desired_custom_roles", Version: 1},
		{Table: "desired_rig_custom_roles", Version: 1},
	}); err != nil {
		return err
	}

	// Cascade: remove junction rows first to avoid FK violation.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM desired_rig_custom_roles WHERE role_name = ?`, roleName,
	); err != nil {
		return fmt.Errorf("delete desired_rig_custom_roles: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM desired_custom_roles WHERE name = ?`, roleName,
	); err != nil {
		return fmt.Errorf("delete desired_custom_roles: %w", err)
	}

	return tx.Commit()
}

// StartStatusSync launches a goroutine that periodically polls actual_custom_roles
// and patches AgentRole status fields. The goroutine exits when ctx is cancelled.
func (r *AgentRoleReconciler) StartStatusSync(ctx context.Context) {
	interval := r.StatusSyncInterval
	if interval == 0 {
		interval = statusSyncIntervalAgentRole
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		logger := log.FromContext(ctx).WithName("agentrole-status-sync")
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

// runStatusSync lists all AgentRoles in all namespaces and, for each one,
// reads actual_custom_roles from Dolt and patches the status fields.
func (r *AgentRoleReconciler) runStatusSync(ctx context.Context) error {
	var arList gasv1alpha1.AgentRoleList
	if err := r.List(ctx, &arList); err != nil {
		return fmt.Errorf("list agentroles: %w", err)
	}

	// Group by (townRef, namespace) to reuse connections.
	type connKey struct{ town, namespace string }
	conns := map[connKey]*doltClient{}
	defer func() {
		for _, d := range conns {
			d.Close()
		}
	}()

	for i := range arList.Items {
		ar := &arList.Items[i]
		key := connKey{ar.Spec.TownRef, ar.Namespace}
		dolt, ok := conns[key]
		if !ok {
			var err error
			dolt, err = openDoltConnection(ctx, r.Client, ar.Spec.TownRef, ar.Namespace)
			if err != nil {
				// Skip this role — Dolt not ready.
				continue
			}
			conns[key] = dolt
		}

		if err := r.patchStatusFromActual(ctx, dolt, ar); err != nil {
			log.FromContext(ctx).Error(err, "patch agentrole status", "name", ar.Name)
		}
	}
	return nil
}

// patchStatusFromActual queries actual_custom_roles and patches the AgentRole status.
func (r *AgentRoleReconciler) patchStatusFromActual(
	ctx context.Context,
	dolt *doltClient,
	ar *gasv1alpha1.AgentRole,
) error {
	const query = `
SELECT active_instances, last_seen_at
FROM actual_custom_roles
WHERE name = ?
LIMIT 1`

	var activeInstances int32
	var lastSeenAt sql.NullTime
	err := dolt.db.QueryRowContext(ctx, query, ar.Name).Scan(&activeInstances, &lastSeenAt)
	if err == sql.ErrNoRows {
		// Role not yet seen in actual topology — leave status as-is.
		return nil
	}
	if err != nil {
		return fmt.Errorf("query actual_custom_roles: %w", err)
	}

	base := ar.DeepCopy()
	ar.Status.ActiveInstances = activeInstances
	if lastSeenAt.Valid {
		t := metav1.NewTime(lastSeenAt.Time)
		ar.Status.LastSeenAt = &t
	}

	// Only patch if something changed.
	if ar.Status.ActiveInstances != base.Status.ActiveInstances ||
		!timeEqual(ar.Status.LastSeenAt, base.Status.LastSeenAt) {
		if err := r.Status().Update(ctx, ar); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
	}
	return nil
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
func (r *AgentRoleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gasv1alpha1.AgentRole{}).
		Complete(r)
}

// setCondition updates or appends a metav1.Condition on the AgentRole status.
func (r *AgentRoleReconciler) setCondition(
	ar *gasv1alpha1.AgentRole,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&ar.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: ar.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	})
}

// emitEvent records a Kubernetes Event against the AgentRole object.
func (r *AgentRoleReconciler) emitEvent(
	ctx context.Context,
	ar *gasv1alpha1.AgentRole,
	eventType, reason, message string,
) {
	// controller-runtime exposes the event recorder via the manager; callers
	// should inject it. For now we log at Info level as a fallback.
	log.FromContext(ctx).Info("event", "type", eventType, "reason", reason, "message", message,
		"agentrole", types.NamespacedName{Name: ar.Name, Namespace: ar.Namespace})
}

// timeEqual returns true when both metav1.Time pointers represent the same instant
// (or are both nil).
func timeEqual(a, b *metav1.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Time.Equal(b.Time)
}
