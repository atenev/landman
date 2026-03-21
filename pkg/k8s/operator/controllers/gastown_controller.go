package controllers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
	"github.com/tenev/dgt/pkg/surveyor"
)

const (
	statusSyncIntervalGasTown = 30 * time.Second

	// defaultSurveyorImage is the container image used when SurveyorImage is unset.
	defaultSurveyorImage = "ghcr.io/tenev/gastown-surveyor:latest"

	// gasTownCleanupFinalizer is set on every GasTown to ensure the desired_town
	// Dolt row is removed when the CR is deleted.
	gasTownCleanupFinalizer = "gastown.io/town-cleanup"
)

// GasTownReconciler reconciles GasTown CRs.
//
// Responsibilities:
//   - Write desired_town rows to Dolt (ADR-0003 compliant).
//   - Manage the Surveyor Deployment when spec.agents.surveyor=true.
//   - Sync actual_town → GasTown status every 30s.
type GasTownReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Recorder           record.EventRecorder
	StatusSyncInterval time.Duration
	// ConnectDolt overrides the Dolt connection factory for testing.
	// When nil, openDoltConnectionFromSpec is used.
	ConnectDolt DoltConnector
}

// +kubebuilder:rbac:groups=gastown.tenev.io,resources=gastowns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=gastowns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=gastowns/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconcile loop for GasTown.
func (r *GasTownReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, retErr error) {
	defer observeReconcile("gastown", time.Now(), &retErr)
	logger := log.FromContext(ctx).WithValues("gastown", req.Name)

	// 1. Fetch the GasTown CR (cluster-scoped, no namespace).
	var gt gasv1alpha1.GasTown
	if err := r.Get(ctx, req.NamespacedName, &gt); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get gastown: %w", err)
	}

	// 2. Handle deletion: remove the desired_town row from Dolt before the
	//    object is garbage-collected.
	if !gt.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &gt)
	}

	// 3. Ensure the cleanup finalizer is registered.
	if !controllerutil.ContainsFinalizer(&gt, gasTownCleanupFinalizer) {
		controllerutil.AddFinalizer(&gt, gasTownCleanupFinalizer)
		if err := r.Update(ctx, &gt); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. Resolve DoltInstance readiness gate.
	//    GasTown is cluster-scoped; DoltRef carries its own namespace.
	connectDolt := r.ConnectDolt
	if connectDolt == nil {
		connectDolt = openDoltConnectionFromSpec
	}
	dolt, err := connectDolt(ctx, r.Client, gt.Spec.DoltRef)
	if err != nil {
		logger.Info("dolt not ready, requeuing", "reason", err.Error())
		r.setCondition(&gt, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltNotReady", err.Error())
		if err := r.Status().Update(ctx, &gt); err != nil {
			logger.Error(err, "update status after dolt not ready")
		}
		r.emitEvent(&gt, corev1.EventTypeWarning, "DoltNotReady", err.Error())
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	defer dolt.Close()

	// 5. Write desired_town to Dolt.
	doltCommit, err := r.syncToDolt(ctx, dolt, &gt)
	if err != nil {
		logger.Error(err, "failed to sync to dolt")
		r.setCondition(&gt, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltWriteFailed", err.Error())
		if err := r.Status().Update(ctx, &gt); err != nil {
			logger.Error(err, "update status after dolt write failed")
		}
		r.emitEvent(&gt, corev1.EventTypeWarning, "DoltWriteFailed", err.Error())
		return ctrl.Result{}, err
	}

	// 6. Reconcile Surveyor Deployment.
	if gt.Spec.Agents.Surveyor {
		if err := r.reconcileSurveyor(ctx, &gt, dolt); err != nil {
			logger.Error(err, "failed to reconcile surveyor deployment")
			r.setCondition(&gt, "SurveyorRunning", metav1.ConditionFalse, "ReconcileError", err.Error())
			if err := r.Status().Update(ctx, &gt); err != nil {
				logger.Error(err, "update status after surveyor reconcile failed")
			}
			r.emitEvent(&gt, corev1.EventTypeWarning, "SurveyorError", err.Error())
			return ctrl.Result{}, err
		}
	}

	// 7. Update status.
	gt.Status.DoltCommit = doltCommit
	gt.Status.ObservedGeneration = gt.Generation
	r.setCondition(&gt, "DesiredTopologyInSync", metav1.ConditionTrue, "Synced",
		"desired_town written successfully")
	if err := r.Status().Update(ctx, &gt); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	r.emitEventf(&gt, corev1.EventTypeNormal, "Synced",
		"desired_town written successfully (commit %s)", doltCommit)
	logger.Info("reconciled", "doltCommit", doltCommit)
	return ctrl.Result{}, nil
}

// syncToDolt writes the GasTown spec to desired_town within a versions-first
// transaction (ADR-0003). Returns the Dolt commit hash.
func (r *GasTownReconciler) syncToDolt(
	ctx context.Context,
	dolt *doltClient,
	gt *gasv1alpha1.GasTown,
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
		{Table: "desired_town", Version: 1},
	}); err != nil {
		return "", err
	}

	// Claim advisory write lock (dgt-lc3).
	if err := upsertTopologyLock(ctx, tx); err != nil {
		return "", err
	}

	const upsertTown = `
INSERT INTO desired_town
  (name, home, mayor_model, polecat_model, max_polecats)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  home          = VALUES(home),
  mayor_model   = VALUES(mayor_model),
  polecat_model = VALUES(polecat_model),
  max_polecats  = VALUES(max_polecats)`

	mayorModel := gt.Spec.Defaults.MayorModel
	if mayorModel == "" {
		mayorModel = "claude-opus-4-6"
	}
	polecatModel := gt.Spec.Defaults.PolecatModel
	if polecatModel == "" {
		polecatModel = "claude-sonnet-4-6"
	}
	maxPolecats := gt.Spec.Defaults.MaxPolecats
	if maxPolecats == 0 {
		maxPolecats = 20
	}

	if _, err := tx.ExecContext(ctx, upsertTown,
		gt.Name,
		gt.Spec.Home,
		mayorModel,
		polecatModel,
		maxPolecats,
	); err != nil {
		return "", fmt.Errorf("upsert desired_town: %w", err)
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

// reconcileSurveyor creates or updates the Surveyor Deployment for this GasTown.
func (r *GasTownReconciler) reconcileSurveyor(
	ctx context.Context,
	gt *gasv1alpha1.GasTown,
	dolt *doltClient,
) error {
	// Surveyor must have a CLAUDE.md ConfigMap.
	if gt.Spec.Agents.SurveyorClaudeMdRef == nil {
		return fmt.Errorf("spec.agents.surveyorClaudeMdRef is required when surveyor=true")
	}

	// Resolve DoltInstance endpoint for the env var.
	var doltInst gasv1alpha1.DoltInstance
	if err := r.Get(ctx, client.ObjectKey{
		Name:      gt.Spec.DoltRef.Name,
		Namespace: gt.Spec.DoltRef.Namespace,
	}, &doltInst); err != nil {
		return fmt.Errorf("get doltinstance for surveyor: %w", err)
	}

	image := gt.Spec.Agents.SurveyorImage
	if image == "" {
		image = defaultSurveyorImage
	}

	var replicas int32 = 1

	// Build Deployment.
	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      surveyorDeploymentName(gt.Name),
			Namespace: gt.Spec.DoltRef.Namespace,
		},
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "surveyor",
				Image: image,
				Env: []corev1.EnvVar{
					{
						Name:  "DOLT_ENDPOINT",
						Value: doltInst.Status.Endpoint,
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "claude-md",
						MountPath: "/gt/CLAUDE.md",
						SubPath:   "CLAUDE.md",
						ReadOnly:  true,
					},
				},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "claude-md",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: gt.Spec.Agents.SurveyorClaudeMdRef.Name,
						},
					},
				},
			},
		},
	}

	// Apply resource requirements if specified.
	if gt.Spec.Agents.SurveyorResources != nil {
		podSpec.Containers[0].Resources = *gt.Spec.Agents.SurveyorResources
	}

	// Add envFrom for secretsRef if configured.
	if gt.Spec.SecretsRef != nil {
		podSpec.Containers[0].EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: gt.Spec.SecretsRef.Name,
					},
				},
			},
		}
	}

	desired.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Selector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"app.kubernetes.io/name":      "surveyor",
				"app.kubernetes.io/instance":  gt.Name,
				"app.kubernetes.io/component": "surveyor",
			},
		},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					"app.kubernetes.io/name":      "surveyor",
					"app.kubernetes.io/instance":  gt.Name,
					"app.kubernetes.io/component": "surveyor",
				},
			},
			Spec: podSpec,
		},
	}

	// Fetch existing Deployment.
	var existing appsv1.Deployment
	err := r.Get(ctx, client.ObjectKey{
		Name:      desired.Name,
		Namespace: desired.Namespace,
	}, &existing)

	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("create surveyor deployment: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get surveyor deployment: %w", err)
	}

	// Update: patch spec if image or env changed.
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	if err := r.Patch(ctx, &existing, patch); err != nil {
		return fmt.Errorf("patch surveyor deployment: %w", err)
	}
	return nil
}

// StartStatusSync launches a goroutine that periodically polls actual_town
// and patches GasTown status fields. The goroutine exits when ctx is cancelled.
func (r *GasTownReconciler) StartStatusSync(ctx context.Context) {
	interval := r.StatusSyncInterval
	if interval == 0 {
		interval = statusSyncIntervalGasTown
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		logger := log.FromContext(ctx).WithName("gastown-status-sync")
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

// runStatusSync lists all GasTowns and, for each, reads actual_town from Dolt
// and patches status fields.
func (r *GasTownReconciler) runStatusSync(ctx context.Context) error {
	var gtList gasv1alpha1.GasTownList
	if err := r.List(ctx, &gtList); err != nil {
		return fmt.Errorf("list gastowns: %w", err)
	}

	type connKey struct{ name, namespace string }
	conns := map[connKey]*doltClient{}
	defer func() {
		for _, d := range conns {
			d.Close()
		}
	}()

	for i := range gtList.Items {
		gt := &gtList.Items[i]
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
		if err := r.patchStatusFromActual(ctx, dolt, gt); err != nil {
			log.FromContext(ctx).Error(err, "patch gastown status", "name", gt.Name)
		}
	}
	return nil
}

// patchStatusFromActual reads actual_town and actual_topology tables, computes
// the convergence score, and patches GasTown status.
func (r *GasTownReconciler) patchStatusFromActual(
	ctx context.Context,
	dolt *doltClient,
	gt *gasv1alpha1.GasTown,
) error {
	const query = `
SELECT last_reconcile_at
FROM actual_town
WHERE name = ?
LIMIT 1`

	var lastReconcileAt sql.NullTime
	err := dolt.db.QueryRowContext(ctx, query, gt.Name).Scan(&lastReconcileAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("query actual_town: %w", err)
	}

	base := gt.DeepCopy()
	if lastReconcileAt.Valid {
		t := metav1.NewTime(lastReconcileAt.Time)
		gt.Status.LastReconcileAt = &t
	}

	// Compute convergence score from topology tables. Errors are non-fatal:
	// log and continue with existing status fields.
	logger := log.FromContext(ctx)
	desired, actual, err := readTopologyForStatus(ctx, dolt.db)
	if err != nil {
		logger.Info("convergence score unavailable", "reason", err.Error())
		r.setCondition(gt, "ActualTopologyAvailable", metav1.ConditionFalse,
			"SurveyorNotStarted", err.Error())
	} else {
		result := surveyor.ComputeScore(desired, actual,
			surveyor.DefaultProductionConfig(), time.Now())

		gt.Status.ConvergenceScore = result.Score
		gt.Status.NonConverged = cappedSlice(result.NonConverged, 20)
		t := metav1.Now()
		gt.Status.LastConvergenceAt = &t

		r.setCondition(gt, "ActualTopologyAvailable", metav1.ConditionTrue,
			"TopologyRead", "actual_topology tables readable")

		if result.Score == 1.0 {
			r.setCondition(gt, "FleetConverged", metav1.ConditionTrue,
				"FullyConverged", "all desired resources are running")
		} else {
			msg := fmt.Sprintf("non-converged: %s", summariseNonConverged(result.NonConverged))
			r.setCondition(gt, "FleetConverged", metav1.ConditionFalse,
				"PartialConvergence", msg)
		}
	}

	statusChanged := !timeEqual(gt.Status.LastReconcileAt, base.Status.LastReconcileAt) ||
		gt.Status.ConvergenceScore != base.Status.ConvergenceScore ||
		!timeEqual(gt.Status.LastConvergenceAt, base.Status.LastConvergenceAt)

	if statusChanged {
		if err := r.Status().Update(ctx, gt); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
	}
	return nil
}

// readTopologyForStatus reads desired_* and actual_* tables from Dolt and
// returns the topology snapshots needed for convergence scoring.
func readTopologyForStatus(
	ctx context.Context,
	db *sql.DB,
) (surveyor.DesiredTopology, surveyor.ActualTopology, error) {
	var desired surveyor.DesiredTopology
	var actual surveyor.ActualTopology

	// Read desired_rigs.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT name, enabled, max_polecats FROM desired_rigs`)
		if err != nil {
			return desired, actual, fmt.Errorf("read desired_rigs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var dr surveyor.DesiredRig
			if err := rows.Scan(&dr.Name, &dr.Enabled, &dr.MaxPolecats); err != nil {
				return desired, actual, fmt.Errorf("scan desired_rigs: %w", err)
			}
			desired.Rigs = append(desired.Rigs, dr)
		}
		if err := rows.Err(); err != nil {
			return desired, actual, err
		}
	}

	// Read desired_agent_config for witness_enabled per rig.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT rig_name, role FROM desired_agent_config`)
		if err == nil {
			defer rows.Close()
			witnessEnabled := make(map[string]bool)
			for rows.Next() {
				var rigName, role string
				if err := rows.Scan(&rigName, &role); err != nil {
					continue
				}
				if role == "witness" {
					witnessEnabled[rigName] = true
				}
			}
			for i := range desired.Rigs {
				desired.Rigs[i].WitnessEnabled = witnessEnabled[desired.Rigs[i].Name]
			}
		}
	}

	// Read desired_custom_roles + desired_rig_custom_roles.
	{
		rows, err := db.QueryContext(ctx, `
SELECT r.name, r.scope, COALESCE(j.rig_name, '__town__') AS rig_name, 0
FROM desired_custom_roles r
LEFT JOIN desired_rig_custom_roles j
  ON r.name = j.role_name AND r.scope = 'rig'
WHERE r.scope = 'town' OR j.rig_name IS NOT NULL`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var dcr surveyor.DesiredCustomRole
				if err := rows.Scan(&dcr.Name, &dcr.Scope, &dcr.RigName, &dcr.InstanceIndex); err != nil {
					continue
				}
				desired.CustomRoles = append(desired.CustomRoles, dcr)
			}
		}
	}

	// Read desired_formulas.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT rig_name, formula_name FROM desired_formulas`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var df surveyor.DesiredFormula
				if err := rows.Scan(&df.RigName, &df.Name); err != nil {
					continue
				}
				desired.Formulas = append(desired.Formulas, df)
			}
		}
	}

	// Read actual_rigs.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT name, enabled, status, last_seen FROM actual_rigs`)
		if err != nil {
			return desired, actual, fmt.Errorf("read actual_rigs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var ar surveyor.RigState
			if err := rows.Scan(&ar.Name, &ar.Enabled, &ar.Status, &ar.LastSeen); err != nil {
				return desired, actual, fmt.Errorf("scan actual_rigs: %w", err)
			}
			actual.Rigs = append(actual.Rigs, ar)
		}
		if err := rows.Err(); err != nil {
			return desired, actual, err
		}
	}

	// Read actual_agent_config.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT rig_name, role, status, last_seen FROM actual_agent_config`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var a surveyor.AgentState
				if err := rows.Scan(&a.RigName, &a.Role, &a.Status, &a.LastSeen); err != nil {
					continue
				}
				actual.Agents = append(actual.Agents, a)
			}
		}
	}

	// Read actual_worktrees.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT rig_name, status, last_seen FROM actual_worktrees`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var wt surveyor.WorktreeState
				if err := rows.Scan(&wt.RigName, &wt.Status, &wt.LastSeen); err != nil {
					continue
				}
				actual.Worktrees = append(actual.Worktrees, wt)
			}
		}
	}

	// Read actual_custom_roles.
	{
		rows, err := db.QueryContext(ctx,
			`SELECT rig_name, role_name, instance_index, status, last_seen FROM actual_custom_roles`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var cr surveyor.CustomRoleState
				if err := rows.Scan(&cr.RigName, &cr.RoleName, &cr.InstanceIndex, &cr.Status, &cr.LastSeen); err != nil {
					continue
				}
				actual.CustomRoles = append(actual.CustomRoles, cr)
			}
		}
	}

	return desired, actual, nil
}

// cappedSlice returns s[:n] when len(s) > n, appending "... and N more" as the
// last entry. Returns s unchanged when len(s) <= n.
func cappedSlice(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	out := make([]string, n)
	copy(out, s[:n-1])
	out[n-1] = fmt.Sprintf("... and %d more", len(s)-(n-1))
	return out
}

// summariseNonConverged returns the first entry (or a count if empty).
func summariseNonConverged(nonConverged []string) string {
	if len(nonConverged) == 0 {
		return "unknown"
	}
	if len(nonConverged) == 1 {
		return nonConverged[0]
	}
	return fmt.Sprintf("%s (and %d more)", nonConverged[0], len(nonConverged)-1)
}

// handleDeletion removes the desired_town Dolt row for the GasTown and then
// removes the finalizer so the object can be garbage-collected. If Dolt is
// unavailable the reconciler requeues rather than failing permanently.
func (r *GasTownReconciler) handleDeletion(
	ctx context.Context,
	gt *gasv1alpha1.GasTown,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(gt, gasTownCleanupFinalizer) {
		return ctrl.Result{}, nil
	}

	connectDolt := r.ConnectDolt
	if connectDolt == nil {
		connectDolt = openDoltConnectionFromSpec
	}
	dolt, err := connectDolt(ctx, r.Client, gt.Spec.DoltRef)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second},
			fmt.Errorf("dolt not ready during deletion: %w", err)
	}
	defer dolt.Close()

	if err := r.deleteFromDolt(ctx, dolt, gt.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete from dolt: %w", err)
	}

	controllerutil.RemoveFinalizer(gt, gasTownCleanupFinalizer)
	if err := r.Update(ctx, gt); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteFromDolt removes the desired_town row for this GasTown within a
// versions-first transaction (ADR-0003).
func (r *GasTownReconciler) deleteFromDolt(
	ctx context.Context,
	dolt *doltClient,
	townName string,
) error {
	tx, err := dolt.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// ADR-0003: versions-first even on delete paths.
	if err := upsertTopologyVersions(ctx, tx, []tableVersion{
		{Table: "desired_town", Version: 1},
	}); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM desired_town WHERE name = ?`, townName); err != nil {
		return fmt.Errorf("delete desired_town: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// SetupWithManager registers the reconciler with the controller-runtime Manager.
func (r *GasTownReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gasv1alpha1.GasTown{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

// setCondition updates or appends a metav1.Condition on the GasTown status.
func (r *GasTownReconciler) setCondition(
	gt *gasv1alpha1.GasTown,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&gt.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: gt.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// surveyorDeploymentName returns the Deployment name for the given GasTown.
func surveyorDeploymentName(gastownName string) string {
	return fmt.Sprintf("%s-surveyor", gastownName)
}

// emitEvent records a Kubernetes Event against the GasTown object.
// It is a no-op when r.Recorder is nil (e.g. in unit tests).
func (r *GasTownReconciler) emitEvent(gt *gasv1alpha1.GasTown, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Event(gt, eventType, reason, message)
}

// emitEventf records a formatted Kubernetes Event against the GasTown object.
// It is a no-op when r.Recorder is nil (e.g. in unit tests).
func (r *GasTownReconciler) emitEventf(gt *gasv1alpha1.GasTown, eventType, reason, messageFmt string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(gt, eventType, reason, messageFmt, args...)
}

// openDoltConnectionFromSpec resolves the DoltInstance directly from a NamespacedRef.
func openDoltConnectionFromSpec(
	ctx context.Context,
	k8s client.Client,
	ref gasv1alpha1.NamespacedRef,
) (*doltClient, error) {
	var doltInst gasv1alpha1.DoltInstance
	if err := k8s.Get(ctx, client.ObjectKey{
		Name:      ref.Name,
		Namespace: ref.Namespace,
	}, &doltInst); err != nil {
		return nil, fmt.Errorf("get doltinstance %q: %w", ref.Name, err)
	}
	if !doltInstanceReady(&doltInst) {
		return nil, fmt.Errorf("doltinstance %q is not ready", ref.Name)
	}
	endpoint := doltInst.Status.Endpoint
	if endpoint == "" {
		return nil, fmt.Errorf("doltinstance %q has empty endpoint", ref.Name)
	}
	dsn := fmt.Sprintf("root@tcp(%s)/gastown?parseTime=true", endpoint)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open dolt connection to %q: %w", endpoint, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping dolt at %q: %w", endpoint, err)
	}
	return &doltClient{db: db}, nil
}
