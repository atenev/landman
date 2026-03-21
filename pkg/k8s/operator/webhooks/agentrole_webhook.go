// Package webhooks implements the validating and mutating admission webhooks
// for Gas Town operator CRDs.
package webhooks

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// cronRE matches a valid 5-field cron expression (minute hour dom month dow).
// Mirrors the pattern in pkg/manifest/validate.go.
var cronRE = regexp.MustCompile(`^(\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+) (\*|[0-9,\-\*\/]+)$`)

// reservedRoleNames is the canonical list of built-in Gas Town agent roles.
// AgentRole CRs may not use any of these names (ADR-0004 D4).
var reservedRoleNames = map[string]bool{
	"mayor":    true,
	"polecat":  true,
	"witness":  true,
	"refinery": true,
	"deacon":   true,
	"dog":      true,
	"crew":     true,
}

// AgentRoleValidator implements the validating webhook for AgentRole.
type AgentRoleValidator struct {
	client.Client
	decoder admission.Decoder
}

// SetupAgentRoleWebhookWithManager registers the AgentRole validator webhook.
func SetupAgentRoleWebhookWithManager(mgr ctrl.Manager) error {
	v := &AgentRoleValidator{Client: mgr.GetClient()}
	mgr.GetWebhookServer().Register(
		"/validate-gastown-tenev-io-v1alpha1-agentrole",
		&webhook.Admission{Handler: v},
	)
	return nil
}

// InjectDecoder satisfies the admission.DecoderInjector interface.
func (v *AgentRoleValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

// Handle validates an AgentRole admission request (Rules 1–4 from spec).
func (v *AgentRoleValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	ar := &gasv1alpha1.AgentRole{}
	if err := v.decoder.DecodeRaw(req.Object, ar); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Rule 1: name not in reserved built-in list (ADR-0004 D4).
	if reservedRoleNames[ar.Name] {
		return admission.Denied(fmt.Sprintf(
			"spec.metadata.name: %q is a reserved built-in Gas Town role name and cannot "+
				"be used for a custom role; choose a different name (ADR-0004)", ar.Name))
	}

	// Rule 2: supervision.parent is non-empty (ADR-0004 D3).
	if ar.Spec.Supervision.Parent == "" {
		return admission.Denied(
			"spec.supervision.parent: required field; every custom role must declare a " +
				"supervision relationship (ADR-0004)")
	}

	// Rule 3: supervision.parent is not self-referential.
	if ar.Spec.Supervision.Parent == ar.Name {
		return admission.Denied(fmt.Sprintf(
			"spec.supervision.parent: %q cannot supervise itself", ar.Name))
	}

	// Rule 4: trigger field consistency (ADR-0004 D6).
	switch ar.Spec.Trigger.Type {
	case "schedule":
		if ar.Spec.Trigger.Schedule == "" {
			return admission.Denied(
				"spec.trigger.schedule: required when spec.trigger.type is 'schedule'")
		}
		if !cronRE.MatchString(ar.Spec.Trigger.Schedule) {
			return admission.Denied(fmt.Sprintf(
				"spec.trigger.schedule: %q is not a valid 5-field cron expression",
				ar.Spec.Trigger.Schedule))
		}
	case "event":
		if ar.Spec.Trigger.Event == "" {
			return admission.Denied(
				"spec.trigger.event: required when spec.trigger.type is 'event'")
		}
	case "bead_assigned", "manual":
		// valid trigger types; no additional field validation required.
	default:
		return admission.Denied(fmt.Sprintf(
			"spec.trigger.type: %q is not a recognized trigger type; "+
				"must be one of: bead_assigned, schedule, event, manual",
			ar.Spec.Trigger.Type))
	}

	return admission.Allowed("")
}

// Ensure AgentRoleValidator implements the handler interface.
var _ admission.Handler = &AgentRoleValidator{}

// AgentRoleDefaulter implements the defaulting webhook for AgentRole.
// Currently a no-op; included for future extension and symmetry with Rig.
type AgentRoleDefaulter struct{}

// Default implements the defaulting webhook logic for AgentRole.
func (d *AgentRoleDefaulter) Default(_ context.Context, obj runtime.Object) error {
	return nil
}
