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
	return &gasv1alpha1.DoltInstance{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "DoltInstance"},
		ObjectMeta: metav1.ObjectMeta{Name: "my-dolt", Namespace: "default"},
		Spec: gasv1alpha1.DoltInstanceSpec{
			Version:  "v1.42.0",
			Replicas: replicas,
			Storage: gasv1alpha1.DoltStorage{
				Size: resource.MustParse("10Gi"),
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
