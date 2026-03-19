package webhooks

import (
	"context"
	"fmt"
	"net/http"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// DoltInstanceValidator implements the validating webhook for DoltInstance (Rule 8 from spec).
type DoltInstanceValidator struct {
	client.Client
	decoder admission.Decoder
}

// SetupDoltInstanceWebhookWithManager registers the DoltInstance validator webhook.
func SetupDoltInstanceWebhookWithManager(mgr ctrl.Manager) error {
	v := &DoltInstanceValidator{Client: mgr.GetClient()}
	mgr.GetWebhookServer().Register(
		"/validate-gastown-tenev-io-v1alpha1-doltinstance",
		&webhook.Admission{Handler: v},
	)
	return nil
}

// InjectDecoder satisfies the admission.DecoderInjector interface.
func (v *DoltInstanceValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

// Handle validates a DoltInstance admission request (Rule 8: replicas==1).
func (v *DoltInstanceValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	di := &gasv1alpha1.DoltInstance{}
	if err := v.decoder.DecodeRaw(req.Object, di); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Rule 8: replicas must be 1 (MVP constraint, ADR-0005 D4).
	if di.Spec.Replicas > 1 {
		return admission.Denied(fmt.Sprintf(
			"spec.replicas: Invalid value: %d: replicas > 1 not supported in "+
				"gastown-operator v0.x", di.Spec.Replicas))
	}

	return admission.Allowed("")
}

// Ensure DoltInstanceValidator implements the handler interface.
var _ admission.Handler = &DoltInstanceValidator{}
