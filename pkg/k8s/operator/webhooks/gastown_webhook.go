package webhooks

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
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
// Rules enforced:
//   - Immutable field on UPDATE: spec.doltRef cannot change after creation.
//   - spec.doltRef.namespace must be non-empty (GasTown is cluster-scoped and
//     cannot supply a default namespace for the referenced DoltInstance).
//   - Rule 6: doltRef resolves to an existing DoltInstance.
//   - Rule 7: surveyor=true requires surveyorClaudeMdRef.
func (v *GasTownValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	gt := &gasv1alpha1.GasTown{}
	if err := v.decoder.DecodeRaw(req.Object, gt); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// spec.doltRef is immutable: re-pointing a GasTown to a different Dolt
	// database mid-lifecycle produces undefined behaviour in controllers and
	// running agents.
	if req.Operation == admissionv1.Update {
		old := &gasv1alpha1.GasTown{}
		if err := v.decoder.DecodeRaw(req.OldObject, old); err != nil {
			return admission.Errored(http.StatusBadRequest,
				fmt.Errorf("decode oldObject: %w", err))
		}
		if gt.Spec.DoltRef != old.Spec.DoltRef {
			return admission.Denied(fmt.Sprintf(
				"spec.doltRef: Forbidden: field is immutable after creation "+
					"(old: %s/%s, new: %s/%s)",
				old.Spec.DoltRef.Namespace, old.Spec.DoltRef.Name,
				gt.Spec.DoltRef.Namespace, gt.Spec.DoltRef.Name))
		}
	}

	// Rule 6: doltRef.Namespace must be explicitly set. GasTown is a
	// cluster-scoped resource and has no namespace to fall back on, so an
	// empty namespace would silently query the wrong bucket.
	doltRef := gt.Spec.DoltRef
	if doltRef.Namespace == "" {
		return admission.Denied(
			"spec.doltRef.namespace: Required value: namespace must not be empty")
	}

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
