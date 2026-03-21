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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
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

// Reconcile is the main reconcile loop for GasTown.
func (r *GasTownReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
		_ = r.Status().Update(ctx, &gt)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	defer dolt.Close()

	// 5. Write desired_town to Dolt.
	doltCommit, err := r.syncToDolt(ctx, dolt, &gt)
	if err != nil {
		logger.Error(err, "failed to sync to dolt")
		r.setCondition(&gt, "DesiredTopologyInSync", metav1.ConditionFalse, "DoltWriteFailed", err.Error())
		_ = r.Status().Update(ctx, &gt)
		return ctrl.Result{}, err
	}

	// 6. Reconcile Surveyor Deployment.
	if gt.Spec.Agents.Surveyor {
		if err := r.reconcileSurveyor(ctx, &gt, dolt); err != nil {
			logger.Error(err, "failed to reconcile surveyor deployment")
			r.setCondition(&gt, "SurveyorRunning", metav1.ConditionFalse, "ReconcileError", err.Error())
			_ = r.Status().Update(ctx, &gt)
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

// patchStatusFromActual reads actual_town and patches GasTown status.
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

	if !timeEqual(gt.Status.LastReconcileAt, base.Status.LastReconcileAt) {
		if err := r.Status().Update(ctx, gt); err != nil {
			return fmt.Errorf("update status: %w", err)
		}
	}
	return nil
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

	return tx.Commit()
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
