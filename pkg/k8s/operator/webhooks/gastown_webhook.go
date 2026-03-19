package webhooks

import (
	"context"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// GasTownValidator implements the validating webhook for GasTown (Rules 6–7 from spec).
type GasTownValidator struct {
	client.Client
	decoder admission.Decoder
}

// SetupGasTownWebhookWithManager registers the GasTown validator webhook.
func SetupGasTownWebhookWithManager(mgr ctrl.Manager) error {
	v := &GasTownValidator{Client: mgr.GetClient()}
	mgr.GetWebhookServer().Register(
		"/validate-gastown-tenev-io-v1alpha1-gastown",
		&webhook.Admission{Handler: v},
	)
	return nil
}

// InjectDecoder satisfies the admission.DecoderInjector interface.
func (v *GasTownValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

// Handle validates a GasTown admission request (Rules 6–7).
func (v *GasTownValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	gt := &gasv1alpha1.GasTown{}
	if err := v.decoder.DecodeRaw(req.Object, gt); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Rule 6: doltRef resolves to an existing DoltInstance.
	doltRef := gt.Spec.DoltRef
	dolt := &gasv1alpha1.DoltInstance{}
	if err := v.Get(ctx, client.ObjectKey{
		Name:      doltRef.Name,
		Namespace: doltRef.Namespace,
	}, dolt); err != nil {
		if apierrors.IsNotFound(err) {
			return admission.Denied(fmt.Sprintf(
				"spec.doltRef: DoltInstance %q not found in namespace %q",
				doltRef.Name, doltRef.Namespace))
		}
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("get doltinstance %q: %w", doltRef.Name, err))
	}

	// Rule 7: surveyor=true requires surveyorClaudeMdRef.
	if gt.Spec.Agents.Surveyor && gt.Spec.Agents.SurveyorClaudeMdRef == nil {
		return admission.Denied(
			"spec.agents.surveyorClaudeMdRef: required when spec.agents.surveyor is true")
	}

	return admission.Allowed("")
}

// Ensure GasTownValidator implements the handler interface.
var _ admission.Handler = &GasTownValidator{}
