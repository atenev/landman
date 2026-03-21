package controllers

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

//go:embed sql/gastown_init_v1.sql
var gastownInitV1SQL string

//go:embed all:sql/migrate
var doltMigrateFS embed.FS

const (
	doltBaseImage    = "dolthub/dolt"
	doltMountDir     = "/var/lib/dolt"
	doltSvcPort      = int32(3306)
	ddlInitSchemaVer = "v1"
	ddlInitSQLKey    = "init.sql"
	jobTTLSecs       = int32(300) // garbage-collect completed Jobs after 5 minutes

	// doltInstanceCleanupFinalizer is set on every DoltInstance to ensure PVCs
	// created by the StatefulSet VolumeClaimTemplates are explicitly deleted on
	// CR removal. Kubernetes does not auto-delete PVCs from VolumeClaimTemplates.
	doltInstanceCleanupFinalizer = "gastown.io/dolt-cleanup"
)

// DoltInstanceReconciler reconciles DoltInstance CRs.
//
// Responsibilities:
//   - Reconcile a StatefulSet (Dolt pod + PVC), a headless Service, and a
//     ClusterIP Service owned by the DoltInstance.
//   - Manage the operator-owned DDL init ConfigMap and its one-shot init Job.
//   - Manage migration Jobs on Dolt binary version upgrades.
//   - Gate other controllers via status.conditions[Ready] and
//     status.conditions[DDLInitialized].
type DoltInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=gastown.tenev.io,resources=doltinstances,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=doltinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=gastown.tenev.io,resources=doltinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is the main reconcile loop for DoltInstance.
func (r *DoltInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("doltinstance", req.NamespacedName)

	// 1. Fetch the DoltInstance CR.
	var di gasv1alpha1.DoltInstance
	if err := r.Get(ctx, req.NamespacedName, &di); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get doltinstance: %w", err)
	}

	// 2. Handle deletion: delete orphaned PVCs before the object is removed.
	if !di.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &di)
	}

	// 3. Ensure the cleanup finalizer is registered.
	if !controllerutil.ContainsFinalizer(&di, doltInstanceCleanupFinalizer) {
		controllerutil.AddFinalizer(&di, doltInstanceCleanupFinalizer)
		if err := r.Update(ctx, &di); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 5. Defensive: reject replicas > 1 (admission webhook is primary).
	if di.Spec.Replicas > 1 {
		r.setCondition(&di, "Ready", metav1.ConditionFalse, "ReplicasUnsupported",
			fmt.Sprintf("spec.replicas: Invalid value: %d: replicas > 1 not supported in"+
				" gastown-operator v0.x; see https://github.com/gastown/operator/issues/XXX"+
				" for replication roadmap", di.Spec.Replicas))
		_ = r.Status().Update(ctx, &di)
		return ctrl.Result{}, nil
	}

	// 6. Ensure the operator-managed DDL init ConfigMap exists.
	if err := r.reconcileDDLConfigMap(ctx, &di); err != nil {
		return ctrl.Result{}, r.recordReadyFalse(ctx, &di, "DDLConfigMapError", err)
	}

	// 7. Reconcile the StatefulSet.
	if err := r.reconcileStatefulSet(ctx, &di); err != nil {
		return ctrl.Result{}, r.recordReadyFalse(ctx, &di, "StatefulSetError", err)
	}

	// 8. Reconcile headless and ClusterIP Services.
	if err := r.reconcileServices(ctx, &di); err != nil {
		return ctrl.Result{}, r.recordReadyFalse(ctx, &di, "ServiceError", err)
	}

	// 9. Publish status.endpoint (stable once Services exist, even before pod ready).
	port := svcPort(&di)
	di.Status.Endpoint = fmt.Sprintf("%s.%s.svc.cluster.local:%d", di.Name, di.Namespace, port)

	// 10. Gate on StatefulSet readiness.
	ready, err := r.statefulSetReady(ctx, &di)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check statefulset ready: %w", err)
	}
	if !ready {
		logger.V(1).Info("waiting for pod to become ready")
		r.setCondition(&di, "Ready", metav1.ConditionFalse, "PodNotReady",
			"waiting for DoltInstance pod to become ready")
		di.Status.ObservedGeneration = di.Generation
		_ = r.Status().Update(ctx, &di)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	r.setCondition(&di, "Ready", metav1.ConditionTrue, "PodReady",
		"DoltInstance pod is ready")

	// 11. Reconcile DDL init Job (schema bootstrap on first startup).
	ddlDone, err := r.reconcileDDLInitJob(ctx, &di)
	if err != nil {
		r.setCondition(&di, "DDLInitialized", metav1.ConditionFalse, "JobFailed", err.Error())
		di.Status.ObservedGeneration = di.Generation
		_ = r.Status().Update(ctx, &di)
		return ctrl.Result{}, err
	}
	if !ddlDone {
		r.setCondition(&di, "DDLInitialized", metav1.ConditionFalse, "JobPending",
			"DDL initialisation Job is in progress")
		di.Status.ObservedGeneration = di.Generation
		_ = r.Status().Update(ctx, &di)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	r.setCondition(&di, "DDLInitialized", metav1.ConditionTrue, "JobComplete",
		"Dolt schema initialised successfully")

	// 12. Handle Dolt binary version migrations (only after DDL is initialised).
	migrating, err := r.reconcileMigration(ctx, &di)
	if err != nil {
		return ctrl.Result{}, err
	}
	if migrating {
		di.Status.ObservedGeneration = di.Generation
		_ = r.Status().Update(ctx, &di)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// 13. Persist the running version once converged.
	if di.Status.CurrentVersion != di.Spec.Version {
		di.Status.CurrentVersion = di.Spec.Version
	}

	di.Status.ObservedGeneration = di.Generation
	if err := r.Status().Update(ctx, &di); err != nil {
		return ctrl.Result{}, fmt.Errorf("update doltinstance status: %w", err)
	}
	logger.Info("reconciled", "endpoint", di.Status.Endpoint, "version", di.Status.CurrentVersion)
	return ctrl.Result{}, nil
}

// ─── DDL init ConfigMap ────────────────────────────────────────────────────────

// reconcileDDLConfigMap ensures the operator-managed ConfigMap carrying the
// embedded init SQL exists and is up-to-date. Owned by the DoltInstance.
func (r *DoltInstanceReconciler) reconcileDDLConfigMap(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ddlConfigMapName(di.Name),
			Namespace: di.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := controllerutil.SetControllerReference(di, cm, r.Scheme); err != nil {
			return err
		}
		cm.Data = map[string]string{ddlInitSQLKey: gastownInitV1SQL}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconcile DDL ConfigMap: %w", err)
	}
	return nil
}

// ─── StatefulSet ───────────────────────────────────────────────────────────────

// reconcileStatefulSet creates or updates the Dolt StatefulSet.
//
// VolumeClaimTemplates are immutable — only the container image and replica
// count are patched on existing StatefulSets.
func (r *DoltInstanceReconciler) reconcileStatefulSet(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) error {
	image := doltImage(di)
	labels := doltLabels(di.Name)
	replicas := di.Spec.Replicas
	if replicas == 0 {
		replicas = 1
	}

	var storageClass *string
	if di.Spec.Storage.StorageClassName != "" {
		sc := di.Spec.Storage.StorageClassName
		storageClass = &sc
	}
	storageSize := di.Spec.Storage.Size
	if storageSize.IsZero() {
		storageSize = resource.MustParse("10Gi")
	}

	var existing appsv1.StatefulSet
	err := r.Get(ctx, client.ObjectKey{Name: di.Name, Namespace: di.Namespace}, &existing)

	if apierrors.IsNotFound(err) {
		sts := buildStatefulSet(di, image, labels, replicas, storageClass, storageSize)
		if err := controllerutil.SetControllerReference(di, sts, r.Scheme); err != nil {
			return fmt.Errorf("set owner ref on statefulset: %w", err)
		}
		if err := r.Create(ctx, sts); err != nil {
			return fmt.Errorf("create statefulset: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get statefulset: %w", err)
	}

	// Update only mutable fields; VolumeClaimTemplates are immutable.
	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec.Template.Spec.Containers[0].Image = image
	existing.Spec.Replicas = &replicas
	if err := r.Patch(ctx, &existing, patch); err != nil {
		return fmt.Errorf("patch statefulset: %w", err)
	}
	return nil
}

func buildStatefulSet(
	di *gasv1alpha1.DoltInstance,
	image string,
	labels map[string]string,
	replicas int32,
	storageClass *string,
	storageSize resource.Quantity,
) *appsv1.StatefulSet {
	initDelay := int32(10)
	readPeriod := int32(5)
	liveDelay := int32(30)
	livePeriod := int32(10)

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      di.Name,
			Namespace: di.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName: headlessName(di.Name),
			Replicas:    &replicas,
			Selector:    &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "dolt",
							Image: image,
							Command: []string{
								"dolt", "sql-server",
								"--host", "0.0.0.0",
								"--port", "3306",
								"--user", "root",
								"--data-dir", doltMountDir,
							},
							Ports: []corev1.ContainerPort{
								{ContainerPort: doltSvcPort},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: doltMountDir},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(doltSvcPort),
									},
								},
								InitialDelaySeconds: initDelay,
								PeriodSeconds:       readPeriod,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									TCPSocket: &corev1.TCPSocketAction{
										Port: intstr.FromInt32(doltSvcPort),
									},
								},
								InitialDelaySeconds: liveDelay,
								PeriodSeconds:       livePeriod,
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "data"},
					Spec: corev1.PersistentVolumeClaimSpec{
						StorageClassName: storageClass,
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: storageSize,
							},
						},
					},
				},
			},
		},
	}
}

// ─── Services ──────────────────────────────────────────────────────────────────

func (r *DoltInstanceReconciler) reconcileServices(ctx context.Context, di *gasv1alpha1.DoltInstance) error {
	if err := r.reconcileHeadlessService(ctx, di); err != nil {
		return fmt.Errorf("headless service: %w", err)
	}
	return r.reconcileClusterIPService(ctx, di)
}

func (r *DoltInstanceReconciler) reconcileHeadlessService(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      headlessName(di.Name),
			Namespace: di.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(di, svc, r.Scheme); err != nil {
			return err
		}
		svc.Spec.ClusterIP = "None"
		svc.Spec.Selector = doltLabels(di.Name)
		svc.Spec.Ports = []corev1.ServicePort{
			{Port: doltSvcPort, Protocol: corev1.ProtocolTCP},
		}
		return nil
	})
	return err
}

func (r *DoltInstanceReconciler) reconcileClusterIPService(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) error {
	port := svcPort(di)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      di.Name,
			Namespace: di.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(di, svc, r.Scheme); err != nil {
			return err
		}
		svcType := corev1.ServiceTypeClusterIP
		if di.Spec.Service.Type != "" {
			svcType = corev1.ServiceType(di.Spec.Service.Type)
		}
		svc.Spec.Type = svcType
		svc.Spec.Selector = doltLabels(di.Name)
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Port:       port,
				TargetPort: intstr.FromInt32(doltSvcPort),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		return nil
	})
	return err
}

// ─── StatefulSet readiness ─────────────────────────────────────────────────────

func (r *DoltInstanceReconciler) statefulSetReady(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) (bool, error) {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Name: di.Name, Namespace: di.Namespace}, &sts); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get statefulset for readiness: %w", err)
	}
	want := di.Spec.Replicas
	if want == 0 {
		want = 1
	}
	return sts.Status.ReadyReplicas >= want, nil
}

// ─── DDL init Job ──────────────────────────────────────────────────────────────

// reconcileDDLInitJob ensures the one-shot DDL init Job has run successfully.
// Returns (true, nil) once the schema is bootstrapped.
// Returns (false, nil) while the Job is still running.
// Returns (false, err) on Job failure.
func (r *DoltInstanceReconciler) reconcileDDLInitJob(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) (bool, error) {
	// Fast path: already initialised in a previous reconcile cycle.
	if conditionTrue(di.Status.Conditions, "DDLInitialized") {
		return true, nil
	}

	jobName := ddlInitJobName(di.Name)
	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Name: jobName, Namespace: di.Namespace}, &job)

	if apierrors.IsNotFound(err) {
		j := buildDDLInitJob(di, jobName)
		if err := controllerutil.SetControllerReference(di, j, r.Scheme); err != nil {
			return false, fmt.Errorf("set owner ref on ddl init job: %w", err)
		}
		if err := r.Create(ctx, j); err != nil {
			return false, fmt.Errorf("create ddl init job: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get ddl init job: %w", err)
	}

	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true, nil
		}
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return false, fmt.Errorf("DDL init Job %q failed after %d attempt(s)",
				jobName, job.Status.Failed)
		}
	}
	return false, nil // still running
}

func buildDDLInitJob(di *gasv1alpha1.DoltInstance, jobName string) *batchv1.Job {
	ttl := jobTTLSecs
	backoff := int32(4)
	port := svcPort(di)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: di.Namespace,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:  "dolt-init",
							Image: fmt.Sprintf("%s:%s", doltBaseImage, di.Spec.Version),
							Command: []string{
								"sh", "-c",
								"dolt sql --host $DOLT_HOST --port $DOLT_PORT < /sql/init.sql",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "DOLT_HOST",
									Value: fmt.Sprintf("%s.%s.svc.cluster.local", di.Name, di.Namespace),
								},
								{
									Name:  "DOLT_PORT",
									Value: fmt.Sprintf("%d", port),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "sql", MountPath: "/sql"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "sql",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: ddlConfigMapName(di.Name),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// ─── Version migration Job ─────────────────────────────────────────────────────

// reconcileMigration handles Dolt binary version upgrades.
// Returns (true, nil) while migration is in progress.
// Returns (false, nil) when no migration is needed or migration completed.
func (r *DoltInstanceReconciler) reconcileMigration(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) (bool, error) {
	cur := di.Status.CurrentVersion
	if cur == "" || cur == di.Spec.Version {
		meta.RemoveStatusCondition(&di.Status.Conditions, "VersionMigrationRequired")
		return false, nil
	}

	jobName := migrateJobName(di.Name, cur, di.Spec.Version)
	var job batchv1.Job
	err := r.Get(ctx, client.ObjectKey{Name: jobName, Namespace: di.Namespace}, &job)

	if apierrors.IsNotFound(err) {
		sqlData, lookupErr := findMigrationSQL(doltMigrateFS, cur, di.Spec.Version)
		if lookupErr != nil {
			log.FromContext(ctx).Info("no embedded migration SQL found, skipping version upgrade",
				"from", cur, "to", di.Spec.Version)
			meta.RemoveStatusCondition(&di.Status.Conditions, "VersionMigrationRequired")
			return false, nil
		}
		// Create a ConfigMap for the migration SQL.
		if err := r.reconcileMigrateConfigMap(ctx, di, cur, di.Spec.Version, sqlData); err != nil {
			return false, err
		}
		j := buildMigrationJob(di, jobName, cur, di.Spec.Version)
		if err := controllerutil.SetControllerReference(di, j, r.Scheme); err != nil {
			return false, fmt.Errorf("set owner ref on migration job: %w", err)
		}
		if err := r.Create(ctx, j); err != nil {
			return false, fmt.Errorf("create migration job: %w", err)
		}
		r.setCondition(di, "VersionMigrationRequired", metav1.ConditionTrue, "MigrationPending",
			fmt.Sprintf("migration from %s to %s is in progress", cur, di.Spec.Version))
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get migration job: %w", err)
	}

	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			// Migration succeeded: flip the StatefulSet to the new image.
			if err := r.updateStatefulSetImage(ctx, di, di.Spec.Version); err != nil {
				return false, err
			}
			meta.RemoveStatusCondition(&di.Status.Conditions, "VersionMigrationRequired")
			return false, nil
		}
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			r.setCondition(di, "VersionMigrationRequired", metav1.ConditionTrue, "MigrationFailed",
				fmt.Sprintf("migration from %s to %s failed; StatefulSet image not updated",
					cur, di.Spec.Version))
			return false, fmt.Errorf("migration Job %q failed", jobName)
		}
	}

	r.setCondition(di, "VersionMigrationRequired", metav1.ConditionTrue, "MigrationRunning",
		fmt.Sprintf("migration from %s to %s is running", cur, di.Spec.Version))
	return true, nil
}

func (r *DoltInstanceReconciler) reconcileMigrateConfigMap(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
	from, to, sqlData string,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      migrateConfigMapName(di.Name, from, to),
			Namespace: di.Namespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		if err := controllerutil.SetControllerReference(di, cm, r.Scheme); err != nil {
			return err
		}
		cm.Data = map[string]string{"migrate.sql": sqlData}
		return nil
	})
	return err
}

func buildMigrationJob(di *gasv1alpha1.DoltInstance, jobName, from, to string) *batchv1.Job {
	ttl := jobTTLSecs
	backoff := int32(2)
	port := svcPort(di)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: di.Namespace,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:  "dolt-migrate",
							Image: fmt.Sprintf("%s:%s", doltBaseImage, from),
							Command: []string{
								"sh", "-c",
								"dolt sql --host $DOLT_HOST --port $DOLT_PORT < /sql/migrate.sql",
							},
							Env: []corev1.EnvVar{
								{
									Name:  "DOLT_HOST",
									Value: fmt.Sprintf("%s.%s.svc.cluster.local", di.Name, di.Namespace),
								},
								{
									Name:  "DOLT_PORT",
									Value: fmt.Sprintf("%d", port),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "sql", MountPath: "/sql"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "sql",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: migrateConfigMapName(di.Name, from, to),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *DoltInstanceReconciler) updateStatefulSetImage(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
	version string,
) error {
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Name: di.Name, Namespace: di.Namespace}, &sts); err != nil {
		return fmt.Errorf("get statefulset for image update: %w", err)
	}
	patch := client.MergeFrom(sts.DeepCopy())
	sts.Spec.Template.Spec.Containers[0].Image = fmt.Sprintf("%s:%s", doltBaseImage, version)
	if err := r.Patch(ctx, &sts, patch); err != nil {
		return fmt.Errorf("patch statefulset image to %s: %w", version, err)
	}
	return nil
}

// ─── Finalizer / deletion ──────────────────────────────────────────────────────

// handleDeletion cleans up resources that are not auto-deleted by Kubernetes
// garbage collection when a DoltInstance CR is removed:
//  1. Deletes PVCs created by the StatefulSet VolumeClaimTemplates (they are
//     not owned by the StatefulSet and are therefore not GC'd automatically).
//  2. Removes the finalizer so the object can be garbage-collected.
func (r *DoltInstanceReconciler) handleDeletion(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(di, doltInstanceCleanupFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.deleteOrphanedPVCs(ctx, di); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete orphaned PVCs: %w", err)
	}

	controllerutil.RemoveFinalizer(di, doltInstanceCleanupFinalizer)
	if err := r.Update(ctx, di); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteOrphanedPVCs deletes all PVCs in the DoltInstance namespace that match
// the Dolt label selector. These are created by the StatefulSet's
// VolumeClaimTemplates and are intentionally not owned by the StatefulSet,
// so Kubernetes does not garbage-collect them automatically.
func (r *DoltInstanceReconciler) deleteOrphanedPVCs(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
) error {
	var pvcList corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcList,
		client.InNamespace(di.Namespace),
		client.MatchingLabels(doltLabels(di.Name)),
	); err != nil {
		return fmt.Errorf("list PVCs: %w", err)
	}
	logger := log.FromContext(ctx)
	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if err := r.Delete(ctx, pvc); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("delete PVC %q: %w", pvc.Name, err)
		}
		logger.Info("deleted orphaned PVC", "pvc", pvc.Name)
	}
	return nil
}

// ─── SetupWithManager ──────────────────────────────────────────────────────────

// SetupWithManager registers the reconciler and watches all owned resource types.
func (r *DoltInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gasv1alpha1.DoltInstance{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

// ─── Status helpers ────────────────────────────────────────────────────────────

func (r *DoltInstanceReconciler) setCondition(
	di *gasv1alpha1.DoltInstance,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	meta.SetStatusCondition(&di.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: di.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// recordReadyFalse sets Ready=False, flushes status, and returns err.
func (r *DoltInstanceReconciler) recordReadyFalse(
	ctx context.Context,
	di *gasv1alpha1.DoltInstance,
	reason string,
	err error,
) error {
	r.setCondition(di, "Ready", metav1.ConditionFalse, reason, err.Error())
	_ = r.Status().Update(ctx, di)
	return err
}

// conditionTrue reports whether the named condition exists with Status=True.
func conditionTrue(conditions []metav1.Condition, condType string) bool {
	for _, c := range conditions {
		if c.Type == condType && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// ─── Name and label helpers ────────────────────────────────────────────────────

func ddlConfigMapName(instanceName string) string {
	return fmt.Sprintf("%s-ddl-%s", instanceName, ddlInitSchemaVer)
}

func ddlInitJobName(instanceName string) string {
	return fmt.Sprintf("%s-ddlinit-%s", instanceName, ddlInitSchemaVer)
}

func headlessName(instanceName string) string {
	return fmt.Sprintf("%s-headless", instanceName)
}

func migrateJobName(instanceName, from, to string) string {
	return fmt.Sprintf("%s-migrate-%s-to-%s", instanceName,
		sanitizeVer(from), sanitizeVer(to))
}

func migrateConfigMapName(instanceName, from, to string) string {
	return fmt.Sprintf("%s-migrate-%s-to-%s-sql", instanceName,
		sanitizeVer(from), sanitizeVer(to))
}

var nonAlphanumRE = regexp.MustCompile(`[^a-z0-9-]`)

// sanitizeVer converts a version string to a valid Kubernetes name segment.
func sanitizeVer(v string) string {
	v = strings.ToLower(strings.TrimPrefix(v, "v"))
	return nonAlphanumRE.ReplaceAllString(v, "-")
}

func doltLabels(instanceName string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":  "dolt",
		"gastown.io/doltinstance": instanceName,
	}
}

// svcPort returns the configured service port, defaulting to 3306.
func svcPort(di *gasv1alpha1.DoltInstance) int32 {
	if di.Spec.Service.Port != 0 {
		return di.Spec.Service.Port
	}
	return doltSvcPort
}

// doltImage returns the Dolt container image to run.
// During a version migration the old image is kept until the migration Job
// succeeds; otherwise spec.version is used.
func doltImage(di *gasv1alpha1.DoltInstance) string {
	if conditionTrue(di.Status.Conditions, "VersionMigrationRequired") &&
		di.Status.CurrentVersion != "" {
		return fmt.Sprintf("%s:%s", doltBaseImage, di.Status.CurrentVersion)
	}
	return fmt.Sprintf("%s:%s", doltBaseImage, di.Spec.Version)
}

// ─── Embedded migration SQL lookup ────────────────────────────────────────────

// findMigrationSQL looks up an embedded migration SQL script for the given
// version transition. Files must be named migrate_<from>_to_<to>.sql under
// the sql/migrate/ directory embedded at build time.
func findMigrationSQL(fsys fs.FS, from, to string) (string, error) {
	name := fmt.Sprintf("sql/migrate/migrate_%s_to_%s.sql",
		sanitizeVer(from), sanitizeVer(to))
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", fmt.Errorf("no embedded migration %s→%s (file %q): %w",
			from, to, name, err)
	}
	return string(data), nil
}
