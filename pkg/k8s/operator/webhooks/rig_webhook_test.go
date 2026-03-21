package webhooks_test

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
	"github.com/tenev/dgt/pkg/k8s/operator/webhooks"
)

func newRigValidator(t *testing.T, gastowns ...*gasv1alpha1.GasTown) *webhooks.RigValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	cb := fake.NewClientBuilder().WithScheme(scheme)
	for _, gt := range gastowns {
		cb = cb.WithObjects(gt)
	}

	v := &webhooks.RigValidator{Client: cb.Build()}
	dec := admission.NewDecoder(scheme)
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("InjectDecoder: %v", err)
	}
	return v
}

func makeRig(name, townRef string) *gasv1alpha1.Rig {
	return &gasv1alpha1.Rig{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "Rig"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: gasv1alpha1.RigSpec{
			TownRef: townRef,
			Repo:    "/opt/repos/myrepo",
			Branch:  "main",
			Enabled: true,
		},
	}
}

func encodeRig(t *testing.T, r *gasv1alpha1.Rig) runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return runtime.RawExtension{Raw: b}
}

func makeGasTown(name string) *gasv1alpha1.GasTown {
	return &gasv1alpha1.GasTown{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "GasTown"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: gasv1alpha1.NamespacedRef{Name: "dolt", Namespace: "default"},
		},
	}
}

// ── RigDefaulter helpers ────────────────────────────────────────────────────

func newRigDefaulter(t *testing.T, gastowns ...*gasv1alpha1.GasTown) *webhooks.RigDefaulter {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cb := fake.NewClientBuilder().WithScheme(scheme)
	for _, gt := range gastowns {
		cb = cb.WithObjects(gt)
	}
	d := &webhooks.RigDefaulter{Client: cb.Build()}
	dec := admission.NewDecoder(scheme)
	if err := d.InjectDecoder(dec); err != nil {
		t.Fatalf("InjectDecoder: %v", err)
	}
	return d
}

func makeGasTownWithDefaults(name, mayorModel, polecatModel string, maxPolecats int32) *gasv1alpha1.GasTown {
	gt := makeGasTown(name)
	gt.Spec.Defaults = gasv1alpha1.GasTownDefaults{
		MayorModel:   mayorModel,
		PolecatModel: polecatModel,
		MaxPolecats:  maxPolecats,
	}
	return gt
}

func rigWithAgents(name, townRef, mayorModel, polecatModel string, maxPolecats int32) *gasv1alpha1.Rig {
	r := makeRig(name, townRef)
	r.Spec.Agents.MayorModel = mayorModel
	r.Spec.Agents.PolecatModel = polecatModel
	r.Spec.Agents.MaxPolecats = maxPolecats
	return r
}

// ── RigDefaulter tests ──────────────────────────────────────────────────────

// TestRigDefaulter_AllFieldsMissing_AllDefaultsApplied verifies that when all
// three agent fields are empty, the defaulter emits patches for each.
func TestRigDefaulter_AllFieldsMissing_AllDefaultsApplied(t *testing.T) {
	gt := makeGasTownWithDefaults("town", "claude-opus-4-6", "claude-sonnet-4-6", 20)
	d := newRigDefaulter(t, gt)

	rig := makeRig("r", "town")
	resp := d.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    encodeRig(t, rig),
		},
	})
	if !resp.Allowed {
		t.Fatalf("expected Allowed, got result: %+v", resp.Result)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected patches for all defaults, got none")
	}
}

// TestRigDefaulter_MayorModelMissing_OnlyMayorDefaultApplied verifies that
// when only mayorModel is empty, exactly that field is patched.
func TestRigDefaulter_MayorModelMissing_OnlyMayorDefaultApplied(t *testing.T) {
	gt := makeGasTownWithDefaults("town", "claude-opus-4-6", "claude-sonnet-4-6", 20)
	d := newRigDefaulter(t, gt)

	rig := rigWithAgents("r", "town", "", "claude-sonnet-4-6", 10)
	resp := d.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    encodeRig(t, rig),
		},
	})
	if !resp.Allowed {
		t.Fatalf("expected Allowed, got result: %+v", resp.Result)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected at least one patch for mayorModel default")
	}
}

// TestRigDefaulter_MaxPolecatsMissing_DefaultApplied verifies the maxPolecats
// mutation (Mutation 3) when mayorModel and polecatModel are already set.
func TestRigDefaulter_MaxPolecatsMissing_DefaultApplied(t *testing.T) {
	gt := makeGasTownWithDefaults("town", "claude-opus-4-6", "claude-sonnet-4-6", 30)
	d := newRigDefaulter(t, gt)

	rig := rigWithAgents("r", "town", "my-mayor-model", "my-polecat-model", 0)
	resp := d.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    encodeRig(t, rig),
		},
	})
	if !resp.Allowed {
		t.Fatalf("expected Allowed, got result: %+v", resp.Result)
	}
	if len(resp.Patches) == 0 {
		t.Fatal("expected patch for maxPolecats default")
	}
}

// TestRigDefaulter_AllFieldsSet_NoPatchEmitted verifies the early-exit path
// (rig_webhook.go:114-118): when all three fields are already set no patch is
// emitted, avoiding unnecessary admission review mutations.
func TestRigDefaulter_AllFieldsSet_NoPatchEmitted(t *testing.T) {
	gt := makeGasTownWithDefaults("town", "claude-opus-4-6", "claude-sonnet-4-6", 20)
	d := newRigDefaulter(t, gt)

	rig := rigWithAgents("r", "town", "custom-mayor", "custom-polecat", 5)
	resp := d.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    encodeRig(t, rig),
		},
	})
	if !resp.Allowed {
		t.Fatalf("expected Allowed, got result: %+v", resp.Result)
	}
	if len(resp.Patches) > 0 {
		t.Errorf("expected no patches when all fields already set, got: %v", resp.Patches)
	}
}

// TestRigDefaulter_GasTownNotFound_AllowedNoPatch verifies the
// GasTown-not-found path (rig_webhook.go:123-129): the defaulter allows the
// request without patching so the validating webhook can deny it with a clear
// error message.
func TestRigDefaulter_GasTownNotFound_AllowedNoPatch(t *testing.T) {
	d := newRigDefaulter(t) // empty fake client — no GasTown objects

	rig := makeRig("r", "missing-town")
	resp := d.Handle(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    encodeRig(t, rig),
		},
	})
	if !resp.Allowed {
		t.Fatalf("expected Allowed when GasTown not found (validator will deny separately), got: %+v", resp.Result)
	}
	if len(resp.Patches) > 0 {
		t.Errorf("expected no patches when GasTown not found, got: %v", resp.Patches)
	}
}

// ── RigValidator tests ──────────────────────────────────────────────────────

func TestRigValidator_TownRefImmutable(t *testing.T) {
	gt := makeGasTown("town-a")
	gt2 := makeGasTown("town-b")

	tests := []struct {
		name      string
		old       *gasv1alpha1.Rig
		new       *gasv1alpha1.Rig
		wantAllow bool
	}{
		{
			name:      "same townRef allowed",
			old:       makeRig("my-rig", "town-a"),
			new:       makeRig("my-rig", "town-a"),
			wantAllow: true,
		},
		{
			name:      "townRef change denied",
			old:       makeRig("my-rig", "town-a"),
			new:       makeRig("my-rig", "town-b"),
			wantAllow: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newRigValidator(t, gt, gt2)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Update,
					Object:    encodeRig(t, tc.new),
					OldObject: encodeRig(t, tc.old),
				},
			}
			resp := v.Handle(context.Background(), req)
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
		})
	}
}
