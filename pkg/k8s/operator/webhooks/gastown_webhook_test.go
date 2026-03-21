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

func newGasTownValidator(t *testing.T, dolts ...*gasv1alpha1.DoltInstance) *webhooks.GasTownValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	cb := fake.NewClientBuilder().WithScheme(scheme)
	for _, di := range dolts {
		cb = cb.WithObjects(di)
	}

	v := &webhooks.GasTownValidator{Client: cb.Build()}
	dec := admission.NewDecoder(scheme)
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("InjectDecoder: %v", err)
	}
	return v
}

func makeGasTownFull(name string, doltRef gasv1alpha1.NamespacedRef) *gasv1alpha1.GasTown {
	return &gasv1alpha1.GasTown{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "GasTown"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: gasv1alpha1.GasTownSpec{
			Version: "1",
			Home:    "/opt/gt",
			DoltRef: doltRef,
		},
	}
}

func makeGasTownDoltInst(name, namespace string) *gasv1alpha1.DoltInstance {
	return &gasv1alpha1.DoltInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "DoltInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
	}
}

func encodeGasTown(t *testing.T, gt *gasv1alpha1.GasTown) runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(gt)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return runtime.RawExtension{Raw: b}
}

func TestGasTownValidator_DoltRefNamespaceRequired(t *testing.T) {
	dolt := makeGasTownDoltInst("dolt-db", "infra")
	v := newGasTownValidator(t, dolt)

	tests := []struct {
		name      string
		gt        *gasv1alpha1.GasTown
		wantAllow bool
	}{
		{
			name:      "empty namespace denied",
			gt:        makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-db", Namespace: ""}),
			wantAllow: false,
		},
		{
			name:      "non-empty namespace with existing DoltInstance allowed",
			gt:        makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-db", Namespace: "infra"}),
			wantAllow: true,
		},
		{
			name:      "non-empty namespace with missing DoltInstance denied",
			gt:        makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "missing", Namespace: "infra"}),
			wantAllow: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Create,
					Object:    encodeGasTown(t, tc.gt),
				},
			}
			resp := v.Handle(context.Background(), req)
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
		})
	}
}

func TestGasTownValidator_DoltRefImmutable(t *testing.T) {
	doltA := makeGasTownDoltInst("dolt-a", "infra")
	doltB := makeGasTownDoltInst("dolt-b", "infra")
	v := newGasTownValidator(t, doltA, doltB)

	tests := []struct {
		name      string
		old       *gasv1alpha1.GasTown
		new       *gasv1alpha1.GasTown
		wantAllow bool
	}{
		{
			name:      "same doltRef allowed",
			old:       makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-a", Namespace: "infra"}),
			new:       makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-a", Namespace: "infra"}),
			wantAllow: true,
		},
		{
			name:      "doltRef name change denied",
			old:       makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-a", Namespace: "infra"}),
			new:       makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-b", Namespace: "infra"}),
			wantAllow: false,
		},
		{
			name:      "doltRef namespace change denied",
			old:       makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-a", Namespace: "infra"}),
			new:       makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-a", Namespace: "other"}),
			wantAllow: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Update,
					Object:    encodeGasTown(t, tc.new),
					OldObject: encodeGasTown(t, tc.old),
				},
			}
			resp := v.Handle(context.Background(), req)
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
		})
	}
}

func TestGasTownValidator_SurveyorRequiresClaudeMdRef(t *testing.T) {
	dolt := makeGasTownDoltInst("dolt-db", "infra")
	v := newGasTownValidator(t, dolt)

	gt := makeGasTownFull("my-town", gasv1alpha1.NamespacedRef{Name: "dolt-db", Namespace: "infra"})
	gt.Spec.Agents.Surveyor = true

	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Object:    encodeGasTown(t, gt),
		},
	}
	resp := v.Handle(context.Background(), req)
	if resp.Allowed {
		t.Errorf("expected Denied for surveyor=true without surveyorClaudeMdRef, got Allowed")
	}

	ref := &gasv1alpha1.LocalRef{Name: "my-claude-md"}
	gt.Spec.Agents.SurveyorClaudeMdRef = ref
	req.Object = encodeGasTown(t, gt)
	resp = v.Handle(context.Background(), req)
	if !resp.Allowed {
		t.Errorf("expected Allowed for surveyor=true with surveyorClaudeMdRef, got Denied: %+v", resp.Result)
	}
}
