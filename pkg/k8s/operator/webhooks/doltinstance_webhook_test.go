package webhooks_test

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
	"github.com/tenev/dgt/pkg/k8s/operator/webhooks"
)

func encodeDoltInstance(t *testing.T, di *gasv1alpha1.DoltInstance) runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(di)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return runtime.RawExtension{Raw: b}
}

func newDoltInstanceValidator(t *testing.T) *webhooks.DoltInstanceValidator {
	t.Helper()
	v := &webhooks.DoltInstanceValidator{}
	scheme := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	dec := admission.NewDecoder(scheme)
	if err := v.InjectDecoder(dec); err != nil {
		t.Fatalf("InjectDecoder: %v", err)
	}
	return v
}

func makeDoltInstance(replicas int32) *gasv1alpha1.DoltInstance {
	return makeDoltInstanceFull(replicas, "v1.42.0", "", "10Gi")
}

func makeDoltInstanceFull(replicas int32, version, storageClass, size string) *gasv1alpha1.DoltInstance {
	return &gasv1alpha1.DoltInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "DoltInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  version,
			Replicas: replicas,
			Storage: gasv1alpha1.DoltStorage{
				StorageClassName: storageClass,
				Size:             resource.MustParse(size),
			},
		},
	}
}

func TestDoltInstanceValidator(t *testing.T) {
	tests := []struct {
		name      string
		replicas  int32
		wantAllow bool
	}{
		{"replicas=0 denied", 0, false},
		{"replicas=1 allowed", 1, true},
		{"replicas=2 denied", 2, false},
		{"replicas=3 denied", 3, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newDoltInstanceValidator(t)
			di := makeDoltInstance(tc.replicas)
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
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
		})
	}
}

func TestDoltInstanceValidator_ImmutableFields(t *testing.T) {
	tests := []struct {
		name      string
		old       *gasv1alpha1.DoltInstance
		new       *gasv1alpha1.DoltInstance
		wantAllow bool
	}{
		{
			name:      "no change allowed",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			wantAllow: true,
		},
		{
			name:      "version upgrade allowed",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.43.0", "standard", "10Gi"),
			wantAllow: true,
		},
		{
			name:      "version downgrade denied",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.41.0", "standard", "10Gi"),
			wantAllow: false,
		},
		{
			name:      "major version downgrade denied",
			old:       makeDoltInstanceFull(1, "v2.0.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.99.0", "standard", "10Gi"),
			wantAllow: false,
		},
		{
			name:      "storageClassName change denied",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.42.0", "premium-ssd", "10Gi"),
			wantAllow: false,
		},
		{
			name:      "storage size change denied",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.42.0", "standard", "20Gi"),
			wantAllow: false,
		},
		{
			name:      "storage size shrink denied",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "20Gi"),
			new:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			wantAllow: false,
		},
		// Mixed v-prefix: parseSemver strips leading 'v', so "1.42.0" and
		// "v1.42.0" must parse identically.
		{
			name:      "mixed prefix upgrade allowed (no-v to v)",
			old:       makeDoltInstanceFull(1, "1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.43.0", "standard", "10Gi"),
			wantAllow: true,
		},
		{
			name:      "mixed prefix downgrade denied (v to no-v)",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "1.41.0", "standard", "10Gi"),
			wantAllow: false,
		},
		{
			name:      "same version UPDATE allowed",
			old:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			wantAllow: true,
		},
		{
			name:      "same version mixed prefix UPDATE allowed",
			old:       makeDoltInstanceFull(1, "1.42.0", "standard", "10Gi"),
			new:       makeDoltInstanceFull(1, "v1.42.0", "standard", "10Gi"),
			wantAllow: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newDoltInstanceValidator(t)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Operation: admissionv1.Update,
					Object:    encodeDoltInstance(t, tc.new),
					OldObject: encodeDoltInstance(t, tc.old),
				},
			}
			resp := v.Handle(context.Background(), req)
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
		})
	}
}
