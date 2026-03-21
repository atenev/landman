package webhooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
	"github.com/tenev/dgt/pkg/k8s/operator/webhooks"
)

func newAgentRoleValidator(t *testing.T, gastowns ...*gasv1alpha1.GasTown) *webhooks.AgentRoleValidator {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := gasv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cb := fake.NewClientBuilder().WithScheme(scheme)
	for _, gt := range gastowns {
		cb = cb.WithObjects(gt)
	}
	v := &webhooks.AgentRoleValidator{Client: cb.Build()}
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
	myTown := makeGasTown("my-town")

	tests := []struct {
		name       string
		ar         *gasv1alpha1.AgentRole
		gastowns   []*gasv1alpha1.GasTown
		wantStatus int32
		wantAllow  bool
	}{
		{
			name:      "valid role",
			ar:        makeAgentRole("analyst", "witness", "bead_assigned", "", ""),
			gastowns:  []*gasv1alpha1.GasTown{myTown},
			wantAllow: true,
		},
		{
			name:       "reserved name mayor",
			ar:         makeAgentRole("mayor", "witness", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "reserved name polecat",
			ar:         makeAgentRole("polecat", "witness", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "reserved name witness",
			ar:         makeAgentRole("witness", "witness", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "reserved name refinery",
			ar:         makeAgentRole("refinery", "witness", "bead_assigned", "", ""),
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
			name:       "reserved name dog",
			ar:         makeAgentRole("dog", "witness", "bead_assigned", "", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
		{
			name:       "reserved name crew",
			ar:         makeAgentRole("crew", "witness", "bead_assigned", "", ""),
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
			gastowns:  []*gasv1alpha1.GasTown{myTown},
			wantAllow: true,
		},
		{
			name:       "invalid cron expression",
			ar:         makeAgentRole("analyst", "witness", "schedule", "not-a-cron", ""),
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
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
			gastowns:  []*gasv1alpha1.GasTown{myTown},
			wantAllow: true,
		},
		{
			name:       "townRef not found",
			ar:         makeAgentRole("analyst", "witness", "bead_assigned", "", ""),
			gastowns:   nil,
			wantStatus: http.StatusForbidden,
			wantAllow:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newAgentRoleValidator(t, tc.gastowns...)
			req := admission.Request{
				AdmissionRequest: admissionv1.AdmissionRequest{
					Object: encodeAgentRole(t, tc.ar),
				},
			}
			resp := v.Handle(context.Background(), req)
			if resp.Allowed != tc.wantAllow {
				t.Errorf("Allowed=%v want %v; result=%+v", resp.Allowed, tc.wantAllow, resp.Result)
			}
			if tc.wantStatus != 0 {
				if resp.Result == nil {
					t.Errorf("Result is nil; want Status=%d", tc.wantStatus)
				} else if resp.Result.Code != tc.wantStatus {
					t.Errorf("Result.Code=%d want %d", resp.Result.Code, tc.wantStatus)
				}
			}
		})
	}
}
