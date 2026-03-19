package webhooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
	"github.com/tenev/dgt/pkg/k8s/operator/webhooks"
)

func newAgentRoleValidator(t *testing.T) *webhooks.AgentRoleValidator {
	t.Helper()
	v := &webhooks.AgentRoleValidator{}
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

func encodeAgentRole(t *testing.T, ar *gasv1alpha1.AgentRole) runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(ar)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return runtime.RawExtension{Raw: b}
}

func makeAgentRole(name, parent, triggerType, schedule, event string) *gasv1alpha1.AgentRole {
	return &gasv1alpha1.AgentRole{
		TypeMeta:   metav1.TypeMeta{APIVersion: "gastown.tenev.io/v1alpha1", Kind: "AgentRole"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: gasv1alpha1.AgentRoleSpec{
			TownRef: "my-town",
			Scope:   "rig",
			Identity: gasv1alpha1.AgentRoleIdentity{
				ClaudeMdRef: gasv1alpha1.LocalRef{Name: "my-claude-md"},
			},
			Trigger: gasv1alpha1.AgentRoleTrigger{
				Type:     triggerType,
				Schedule: schedule,
				Event:    event,
			},
			Supervision: gasv1alpha1.AgentRoleSupervision{
				Parent: parent,
			},
		},
	}
}

func TestAgentRoleValidator(t *testing.T) {
	tests := []struct {
		name       string
		ar         *gasv1alpha1.AgentRole
		wantStatus int32
		wantAllow  bool
	}{
		{
			name:      "valid role",
			ar:        makeAgentRole("analyst", "witness", "bead_assigned", "", ""),
			wantAllow: true,
		},
		{
			name:       "reserved name mayor",
			ar:         makeAgentRole("mayor", "witness", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "reserved name deacon",
			ar:         makeAgentRole("deacon", "witness", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "empty supervision parent",
			ar:         makeAgentRole("analyst", "", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "self-referential parent",
			ar:         makeAgentRole("analyst", "analyst", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "schedule type without schedule",
			ar:         makeAgentRole("analyst", "witness", "schedule", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:      "schedule type with schedule",
			ar:        makeAgentRole("analyst", "witness", "schedule", "0 * * * *", ""),
			wantAllow: true,
		},
		{
			name:       "event type without event",
			ar:         makeAgentRole("analyst", "witness", "event", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:      "event type with event",
			ar:        makeAgentRole("analyst", "witness", "event", "", "pr.opened"),
			wantAllow: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newAgentRoleValidator(t)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Object: encodeAgentRole(t, tc.ar),
				},
			}
			resp := v.Handle(context.Background(), req)
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
		})
	}
}
