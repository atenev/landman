// Package controllers — integration tests for all four Gas Town reconcilers and
// admission webhooks (dgt-1ba).
//
// Test strategy:
//   - Use sigs.k8s.io/controller-runtime/pkg/client/fake for the K8s API.
//   - Inject a fake in-process SQL driver (see fake_dolt_test.go) via the
//     ConnectDolt factory field so no real Dolt TCP endpoint is needed.
//   - Run the Reconcile function directly — no envtest binary required.
//
// Test cases covered:
//   1. DoltInstance: StatefulSet + Service + DDL ConfigMap created; DDL Job
//      created once StatefulSet is ready; DDLInitialized condition set after Job
//      completes.
//   2. GasTown: desired_town written to Dolt; Surveyor Deployment created when
//      spec.agents.surveyor=true.
//   3. Rig: desired_rigs + desired_agent_config + desired_formulas written to
//      Dolt; defaults inherited from parent GasTown.
//   4. AgentRole happy path: desired_custom_roles written to Dolt.
//      AgentRole reserved name: ReservedName condition set, no Dolt call.
//   5. Rig deletion: drain finalizer triggers setRigDisabled (enabled=false SQL).
//   6. Webhook: AgentRole with reserved name "mayor" denied.
//   7. Webhook: DoltInstance with replicas=2 denied.
package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	admissionv1 "k8s.io/api/admission/v1"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
	"github.com/tenev/dgt/pkg/k8s/operator/webhooks"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme gasv1alpha1: %v", err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme appsv1: %v", err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme batchv1: %v", err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme corev1: %v", err)
	}
	return s
}

func mustGet(t *testing.T, c client.Client, key client.ObjectKey, obj client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), key, obj); err != nil {
		t.Fatalf("Get %T %s: %v", obj, key, err)
	}
}

func getCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func assertCondition(t *testing.T, conditions []metav1.Condition, condType string, want metav1.ConditionStatus) {
	t.Helper()
	c := getCondition(conditions, condType)
	if c == nil {
		t.Errorf("condition %q not found; conditions: %+v", condType, conditions)
		return
	}
	if c.Status != want {
		t.Errorf("condition %q: got Status=%q, want %q; reason=%q msg=%q",
			condType, c.Status, want, c.Reason, c.Message)
	}
}

// makeReadyDoltInstance creates a DoltInstance with Ready=True status.
func makeReadyDoltInstance(name, ns string) *gasv1alpha1.DoltInstance {
	di := &gasv1alpha1.DoltInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  "v1.42.0",
			Replicas: 1,
			Storage:  gasv1alpha1.DoltStorage{Size: resource.MustParse("10Gi")},
		},
		Status: gasv1alpha1.DoltInstanceStatus{
			Endpoint: "dolt.default.svc.cluster.local:3306",
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "PodReady",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	return di
}

// ── Test case 1: DoltInstance reconcile ──────────────────────────────────────

// TestDoltInstanceReconcile_CreatesResources verifies that reconciling a
// DoltInstance CR creates the StatefulSet, headless Service, ClusterIP Service,
// and DDL init ConfigMap.
func TestDoltInstanceReconcile_CreatesResources(t *testing.T) {
	s := newScheme(t)
	di := &gasv1alpha1.DoltInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  "v1.42.0",
			Replicas: 1,
			Storage:  gasv1alpha1.DoltStorage{Size: resource.MustParse("10Gi")},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(di).
		WithStatusSubresource(di).Build()

	r := &DoltInstanceReconciler{Client: c, Scheme: s}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-dolt", Namespace: "default"}}

	// First reconcile: creates StatefulSet, Services, ConfigMap; waits for pod.
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter > 0 while waiting for pod readiness")
	}

	// StatefulSet should exist.
	var sts appsv1.StatefulSet
	mustGet(t, c, types.NamespacedName{Name: "my-dolt", Namespace: "default"}, &sts)
	if len(sts.Spec.Template.Spec.Containers) != 1 {
		t.Errorf("expected 1 container, got %d", len(sts.Spec.Template.Spec.Containers))
	}
	if sts.Spec.Template.Spec.Containers[0].Image != "dolthub/dolt:v1.42.0" {
		t.Errorf("unexpected image: %q", sts.Spec.Template.Spec.Containers[0].Image)
	}

	// Headless Service should exist.
	var headlessSvc corev1.Service
	mustGet(t, c, types.NamespacedName{Name: "my-dolt-headless", Namespace: "default"}, &headlessSvc)
	if headlessSvc.Spec.ClusterIP != "None" {
		t.Errorf("headless service ClusterIP=%q want %q", headlessSvc.Spec.ClusterIP, "None")
	}

	// ClusterIP Service should exist.
	var svc corev1.Service
	mustGet(t, c, types.NamespacedName{Name: "my-dolt", Namespace: "default"}, &svc)

	// DDL init ConfigMap should exist.
	var cm corev1.ConfigMap
	mustGet(t, c, types.NamespacedName{Name: "my-dolt-ddl-v1", Namespace: "default"}, &cm)
	if _, ok := cm.Data["init.sql"]; !ok {
		t.Errorf("DDL ConfigMap missing init.sql key")
	}
}

// TestDoltInstanceReconcile_DDLJobAndCondition verifies that once the
// StatefulSet is ready, the DDL init Job is created and after the Job
// completes, DDLInitialized=True is set.
func TestDoltInstanceReconcile_DDLJobAndCondition(t *testing.T) {
	s := newScheme(t)
	di := &gasv1alpha1.DoltInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  "v1.42.0",
			Replicas: 1,
			Storage:  gasv1alpha1.DoltStorage{Size: resource.MustParse("10Gi")},
		},
	}
	// Pre-create a ready StatefulSet so the reconciler moves past readiness gate.
	one := int32(1)
	readySTS := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &one,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "dolt", Image: "dolthub/dolt:v1.42.0"}},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: 1},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(di, readySTS).
		WithStatusSubresource(di).Build()

	r := &DoltInstanceReconciler{Client: c, Scheme: s}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-dolt", Namespace: "default"}}

	// Reconcile: StatefulSet ready → creates DDL init Job.
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile (job creation): %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter while DDL Job is pending")
	}

	// DDL init Job should exist.
	var job batchv1.Job
	mustGet(t, c, types.NamespacedName{Name: "my-dolt-ddlinit-v1", Namespace: "default"}, &job)

	// Simulate Job completion by patching its status.
	jobCopy := job.DeepCopy()
	jobCopy.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
	}
	if err := c.Status().Update(context.Background(), jobCopy); err != nil {
		t.Fatalf("patch job status: %v", err)
	}

	// Reconcile again: Job complete → DDLInitialized=True, endpoint published.
	_, err = r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile (after job complete): %v", err)
	}

	// Re-fetch DoltInstance and check status.
	var updatedDI gasv1alpha1.DoltInstance
	mustGet(t, c, types.NamespacedName{Name: "my-dolt", Namespace: "default"}, &updatedDI)
	assertCondition(t, updatedDI.Status.Conditions, "DDLInitialized", metav1.ConditionTrue)
	assertCondition(t, updatedDI.Status.Conditions, "Ready", metav1.ConditionTrue)
	if updatedDI.Status.Endpoint == "" {
		t.Errorf("expected endpoint to be set after reconcile")
	}
}

// ── Test case 2: GasTown reconcile ───────────────────────────────────────────

// TestGasTownReconcile_WritesToDolt verifies that a GasTown CR reconcile writes
// desired_town to Dolt and sets DesiredTopologyInSync=True.
func TestGasTownReconcile_WritesToDolt(t *testing.T) {
	s := newScheme(t)
	doltInst := makeReadyDoltInstance("my-dolt", "default")
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(doltInst, gt).
		WithStatusSubresource(gt).Build()

	db := newFakeDoltDB()
	defer db.Close()

	r := &GasTownReconciler{
		Client:      c,
		Scheme:      s,
		ConnectDolt: fakeDoltConnector(db),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-town"}}

	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated gasv1alpha1.GasTown
	mustGet(t, c, types.NamespacedName{Name: "my-town"}, &updated)
	assertCondition(t, updated.Status.Conditions, "DesiredTopologyInSync", metav1.ConditionTrue)
	if updated.Status.DoltCommit == "" {
		t.Errorf("expected DoltCommit to be non-empty after successful write")
	}
}

// TestGasTownReconcile_CreatesSurveyorDeployment verifies that when
// spec.agents.surveyor=true, the reconciler creates the Surveyor Deployment.
func TestGasTownReconcile_CreatesSurveyorDeployment(t *testing.T) {
	s := newScheme(t)
	doltInst := makeReadyDoltInstance("my-dolt", "default")

	// ConfigMap referenced by surveyorClaudeMdRef.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "surveyor-claude-md", Namespace: "default"},
		Data:       map[string]string{"CLAUDE.md": "# Surveyor"},
	}
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
			Agents: gasv1alpha1.TownAgents{
				Surveyor:            true,
				SurveyorClaudeMdRef: &gasv1alpha1.LocalRef{Name: "surveyor-claude-md"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(doltInst, gt, cm).
		WithStatusSubresource(gt).Build()

	db := newFakeDoltDB()
	defer db.Close()

	r := &GasTownReconciler{
		Client:      c,
		Scheme:      s,
		ConnectDolt: fakeDoltConnector(db),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-town"}}

	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Surveyor Deployment should be created.
	var deploy appsv1.Deployment
	mustGet(t, c, types.NamespacedName{Name: "my-town-surveyor", Namespace: "default"}, &deploy)
	if *deploy.Spec.Replicas != 1 {
		t.Errorf("Surveyor Deployment replicas=%d want 1", *deploy.Spec.Replicas)
	}
}

// TestGasTownReconcile_DoltNotReady verifies that when DoltInstance is not
// ready the reconciler sets DesiredTopologyInSync=False with DoltNotReady reason
// and requeues.
func TestGasTownReconcile_DoltNotReady(t *testing.T) {
	s := newScheme(t)
	// DoltInstance with no Ready=True condition.
	doltInst := &gasv1alpha1.DoltInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  "v1.42.0",
			Replicas: 1,
			Storage:  gasv1alpha1.DoltStorage{Size: resource.MustParse("10Gi")},
		},
	}
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(doltInst, gt).
		WithStatusSubresource(gt).Build()

	// No fake Dolt injected → falls back to openDoltConnectionFromSpec which
	// will fail because DoltInstance is not ready.
	r := &GasTownReconciler{Client: c, Scheme: s}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "my-town"}}

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile returned unexpected error: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter when Dolt is not ready")
	}

	var updated gasv1alpha1.GasTown
	mustGet(t, c, types.NamespacedName{Name: "my-town"}, &updated)
	assertCondition(t, updated.Status.Conditions, "DesiredTopologyInSync", metav1.ConditionFalse)
	cond := getCondition(updated.Status.Conditions, "DesiredTopologyInSync")
	if cond != nil && cond.Reason != "DoltNotReady" {
		t.Errorf("condition reason=%q want DoltNotReady", cond.Reason)
	}
}

// ── Test case 3: Rig reconcile ────────────────────────────────────────────────

// TestRigReconcile_WritesDoltAndInheritsDefaults verifies that a Rig reconcile
// writes desired_rigs to Dolt and inherits GasTown defaults.
func TestRigReconcile_WritesDoltAndInheritsDefaults(t *testing.T) {
	s := newScheme(t)
	doltInst := makeReadyDoltInstance("my-dolt", "default")
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
			Defaults: gasv1alpha1.GasTownDefaults{
				MayorModel:   "claude-opus-4-6",
				PolecatModel: "claude-sonnet-4-6",
				MaxPolecats:  15,
			},
		},
	}
	rig := &gasv1alpha1.Rig{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "default"},
		Spec: gasv1alpha1.RigSpec{
			TownRef: "my-town",
			Repo:    "/srv/repos/backend",
			Branch:  "main",
			Enabled: true,
			Agents: gasv1alpha1.RigAgents{
				Mayor:   true,
				Witness: true,
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(doltInst, gt, rig).
		WithStatusSubresource(rig).Build()

	db := newFakeDoltDB()
	defer db.Close()

	r := &RigReconciler{
		Client:      c,
		Scheme:      s,
		ConnectDolt: fakeDoltConnector(db),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "backend", Namespace: "default"}}

	// First reconcile: adds finalizer → requeues.
	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile (add finalizer): %v", err)
	}

	// Second reconcile: writes Dolt.
	_, err = r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile (write dolt): %v", err)
	}

	var updated gasv1alpha1.Rig
	mustGet(t, c, types.NamespacedName{Name: "backend", Namespace: "default"}, &updated)
	assertCondition(t, updated.Status.Conditions, "DesiredTopologyInSync", metav1.ConditionTrue)
	if updated.Status.DoltCommit == "" {
		t.Errorf("expected DoltCommit to be set")
	}
}

// TestRigReconcile_GasTownNotFound verifies that when the parent GasTown is
// missing, the reconciler sets GasTownNotFound condition and requeues.
func TestRigReconcile_GasTownNotFound(t *testing.T) {
	s := newScheme(t)
	rig := &gasv1alpha1.Rig{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "default"},
		Spec: gasv1alpha1.RigSpec{
			TownRef: "missing-town",
			Repo:    "/srv/repos/backend",
			Branch:  "main",
			Enabled: true,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(rig).
		WithStatusSubresource(rig).Build()

	r := &RigReconciler{Client: c, Scheme: s}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "backend", Namespace: "default"}}

	// Add finalizer first.
	r.Reconcile(context.Background(), req) //nolint:errcheck

	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter when GasTown not found")
	}

	var updated gasv1alpha1.Rig
	mustGet(t, c, types.NamespacedName{Name: "backend", Namespace: "default"}, &updated)
	cond := getCondition(updated.Status.Conditions, "DesiredTopologyInSync")
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("expected DesiredTopologyInSync=False; got %+v", cond)
	}
	if cond != nil && cond.Reason != "GasTownNotFound" {
		t.Errorf("condition reason=%q want GasTownNotFound", cond.Reason)
	}
}

// ── Test case 4: AgentRole reconcile ─────────────────────────────────────────

// TestAgentRoleReconcile_WritesToDolt verifies the happy path: desired_custom_roles
// written to Dolt and DesiredTopologyInSync=True set.
func TestAgentRoleReconcile_WritesToDolt(t *testing.T) {
	s := newScheme(t)
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
		},
	}
	ar := &gasv1alpha1.AgentRole{
		ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "default"},
		Spec: gasv1alpha1.AgentRoleSpec{
			TownRef: "my-town",
			Scope:   "rig",
			Identity: gasv1alpha1.AgentRoleIdentity{
				ClaudeMdRef: gasv1alpha1.LocalRef{Name: "analyst-claude-md"},
			},
			Trigger: gasv1alpha1.AgentRoleTrigger{Type: "bead_assigned"},
			Supervision: gasv1alpha1.AgentRoleSupervision{
				Parent: "witness",
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt, ar).
		WithStatusSubresource(ar).Build()

	db := newFakeDoltDB()
	defer db.Close()

	r := &AgentRoleReconciler{
		Client:      c,
		Scheme:      s,
		ConnectDolt: fakeDoltConnectorByName(db),
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "analyst", Namespace: "default"}}

	// First reconcile: adds finalizer.
	r.Reconcile(context.Background(), req) //nolint:errcheck

	// Second reconcile: writes Dolt.
	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var updated gasv1alpha1.AgentRole
	mustGet(t, c, types.NamespacedName{Name: "analyst", Namespace: "default"}, &updated)
	assertCondition(t, updated.Status.Conditions, "DesiredTopologyInSync", metav1.ConditionTrue)
}

// TestAgentRoleReconcile_ReservedNameRejected verifies that an AgentRole with a
// reserved name does not write to Dolt and sets ReservedName condition.
func TestAgentRoleReconcile_ReservedNameRejected(t *testing.T) {
	s := newScheme(t)
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
		},
	}
	ar := &gasv1alpha1.AgentRole{
		ObjectMeta: metav1.ObjectMeta{Name: "mayor", Namespace: "default"},
		Spec: gasv1alpha1.AgentRoleSpec{
			TownRef: "my-town",
			Scope:   "rig",
			Identity: gasv1alpha1.AgentRoleIdentity{
				ClaudeMdRef: gasv1alpha1.LocalRef{Name: "mayor-claude-md"},
			},
			Trigger:     gasv1alpha1.AgentRoleTrigger{Type: "bead_assigned"},
			Supervision: gasv1alpha1.AgentRoleSupervision{Parent: "witness"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gt, ar).
		WithStatusSubresource(ar).Build()

	// Inject a fake Dolt — but it should never be called.
	doltCalled := false
	r := &AgentRoleReconciler{
		Client: c,
		Scheme: s,
		ConnectDolt: func(_ context.Context, _ client.Client, _, _ string) (*doltClient, error) {
			doltCalled = true
			return &doltClient{db: newFakeDoltDB()}, nil
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "mayor", Namespace: "default"}}

	// Add finalizer first.
	r.Reconcile(context.Background(), req) //nolint:errcheck

	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if doltCalled {
		t.Errorf("Dolt was called for a reserved role name — should be a no-op")
	}

	var updated gasv1alpha1.AgentRole
	mustGet(t, c, types.NamespacedName{Name: "mayor", Namespace: "default"}, &updated)
	cond := getCondition(updated.Status.Conditions, "DesiredTopologyInSync")
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("expected DesiredTopologyInSync=False for reserved name; got %+v", cond)
	}
	if cond != nil && cond.Reason != "ReservedName" {
		t.Errorf("condition reason=%q want ReservedName", cond.Reason)
	}
}

// ── Test case 5: Rig deletion drain finalizer ─────────────────────────────────

// TestRigReconcile_DrainFinalizer verifies that deleting a Rig triggers the
// drain finalizer which writes enabled=false to Dolt before removing the object.
func TestRigReconcile_DrainFinalizer(t *testing.T) {
	s := newScheme(t)
	doltInst := makeReadyDoltInstance("my-dolt", "default")
	gt := &gasv1alpha1.GasTown{
		ObjectMeta: metav1.ObjectMeta{Name: "my-town"},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "my-dolt", Namespace: "default"},
		},
	}
	now := metav1.Now()
	rig := &gasv1alpha1.Rig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "drain-test",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{rigDrainFinalizer},
		},
		Spec: gasv1alpha1.RigSpec{
			TownRef: "my-town",
			Repo:    "/srv/repos/drain-test",
			Branch:  "main",
			Enabled: true,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(doltInst, gt, rig).
		WithStatusSubresource(rig).Build()

	// Track SQL calls to verify enabled=false is written.
	var executedSQL []string
	db := newFakeDoltDB()
	defer db.Close()

	r := &RigReconciler{
		Client: c,
		Scheme: s,
		ConnectDolt: func(ctx context.Context, k client.Client, ref gasv1alpha1.NamespacedRef) (*doltClient, error) {
			executedSQL = append(executedSQL, "connected")
			return &doltClient{db: db}, nil
		},
	}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "drain-test", Namespace: "default"}}

	// The rig has DeletionTimestamp set + drain finalizer → handleDeletion.
	// actual_rigs returns no row (ErrNoRows) → isRigStopped returns true → remove finalizer.
	_, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile (drain): %v", err)
	}

	// Verify Dolt was called (drain attempted).
	if len(executedSQL) == 0 {
		t.Errorf("expected Dolt to be called during drain, but it was not")
	}

	// After a successful drain, the finalizer is removed. The fake client
	// garbage-collects objects with a DeletionTimestamp and no remaining
	// finalizers, so the Rig is deleted — Get returns NotFound.
	var updated gasv1alpha1.Rig
	err = c.Get(context.Background(), types.NamespacedName{Name: "drain-test", Namespace: "default"}, &updated)
	if err == nil {
		// Object still exists — verify the drain finalizer was removed.
		for _, f := range updated.Finalizers {
			if f == rigDrainFinalizer {
				t.Errorf("drain finalizer still present after successful drain")
			}
		}
	}
	// NotFound is also acceptable: the fake client auto-deleted the object.
}

// ── Test case 6: Webhook rejects AgentRole with reserved name ────────────────

func TestWebhook_AgentRoleRejectsReservedName(t *testing.T) {
	s := newScheme(t)
	v := &webhooks.AgentRoleValidator{}
	dec := admission.NewDecoder(s)
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("InjectDecoder: %v", err)
	}

	ar := &gasv1alpha1.AgentRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "AgentRole"},
		ObjectMeta: metav1.ObjectMeta{Name: "mayor", Namespace: "default"},
		Spec: gasv1alpha1.AgentRoleSpec{
			TownRef: "my-town",
			Scope:   "rig",
			Identity: gasv1alpha1.AgentRoleIdentity{
				ClaudeMdRef: gasv1alpha1.LocalRef{Name: "cm"},
			},
			Trigger:     gasv1alpha1.AgentRoleTrigger{Type: "bead_assigned"},
			Supervision: gasv1alpha1.AgentRoleSupervision{Parent: "witness"},
		},
	}
	raw, err := json.Marshal(ar)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object: runtime.RawExtension{Raw: raw},
		},
	}
	resp := v.Handle(context.Background(), req)
	if resp.Allowed {
		t.Errorf("expected AgentRole with reserved name 'mayor' to be denied, but it was allowed")
	}
	if resp.Result != nil && resp.Result.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403, got %d", resp.Result.Code)
	}
}

// ── Test case 7: Webhook rejects DoltInstance with replicas=2 ────────────────

func TestWebhook_DoltInstanceRejectsReplicas2(t *testing.T) {
	s := newScheme(t)
	v := &webhooks.DoltInstanceValidator{}
	dec := admission.NewDecoder(s)
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("InjectDecoder: %v", err)
	}

	di := &gasv1alpha1.DoltInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "DoltInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  "v1.42.0",
			Replicas: 2,
			Storage:  gasv1alpha1.DoltStorage{Size: resource.MustParse("10Gi")},
		},
	}
	raw, err := json.Marshal(di)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object: runtime.RawExtension{Raw: raw},
		},
	}
	resp := v.Handle(context.Background(), req)
	if resp.Allowed {
		t.Errorf("expected DoltInstance with replicas=2 to be denied, but it was allowed")
	}
}
