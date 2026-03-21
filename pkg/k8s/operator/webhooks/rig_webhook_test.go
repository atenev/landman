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
