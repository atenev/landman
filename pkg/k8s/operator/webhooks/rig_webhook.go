package webhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	gasv1alpha1 "github.com/tenev/dgt/pkg/k8s/operator/v1alpha1"
)

// RigValidator implements the validating webhook for Rig (Rule 5 from spec).
type RigValidator struct {
	client.Client
	decoder admission.Decoder
}

// SetupRigWebhookWithManager registers the Rig validator and defaulter webhooks.
func SetupRigWebhookWithManager(mgr ctrl.Manager) error {
	v := &RigValidator{Client: mgr.GetClient()}
	mgr.GetWebhookServer().Register(
		"/validate-gastown-tenev-io-v1alpha1-rig",
		&webhook.Admission{Handler: v},
	)

	d := &RigDefaulter{Client: mgr.GetClient()}
	mgr.GetWebhookServer().Register(
		"/mutate-gastown-tenev-io-v1alpha1-rig",
		&webhook.Admission{Handler: d},
	)
	return nil
}

// InjectDecoder satisfies the admission.DecoderInjector interface.
func (v *RigValidator) InjectDecoder(d admission.Decoder) error {
	v.decoder = d
	return nil
}

// Handle validates a Rig admission request (Rule 5: townRef must resolve).
func (v *RigValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	rig := &gasv1alpha1.Rig{}
	if err := v.decoder.DecodeRaw(req.Object, rig); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Rule 5: townRef resolves to an existing GasTown.
	gastownList := &gasv1alpha1.GasTownList{}
	if err := v.List(ctx, gastownList); err != nil {
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("list gastowns: %w", err))
	}
	found := false
	for i := range gastownList.Items {
		if gastownList.Items[i].Name == rig.Spec.TownRef {
			found = true
			break
		}
	}
	if !found {
		return admission.Denied(fmt.Sprintf(
			"spec.townRef: GasTown %q not found in cluster", rig.Spec.TownRef))
	}

	return admission.Allowed("")
}

// Ensure RigValidator implements the handler interface.
var _ admission.Handler = &RigValidator{}

// RigDefaulter implements the mutating webhook for Rig.
// Applies defaults from the parent GasTown (Mutations 1–3 from spec).
type RigDefaulter struct {
	client.Client
	decoder admission.Decoder
}

// InjectDecoder satisfies the admission.DecoderInjector interface.
func (d *RigDefaulter) InjectDecoder(dec admission.Decoder) error {
	d.decoder = dec
	return nil
}

// Handle applies Rig defaults from the parent GasTown at admission time.
func (d *RigDefaulter) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithName("rig-defaulter")

	rig := &gasv1alpha1.Rig{}
	if err := d.decoder.DecodeRaw(req.Object, rig); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Only default if at least one field is missing.
	if rig.Spec.Agents.MayorModel != "" &&
		rig.Spec.Agents.PolecatModel != "" &&
		rig.Spec.Agents.MaxPolecats != 0 {
		return admission.Allowed("")
	}

	// Fetch parent GasTown defaults.
	gastown := &gasv1alpha1.GasTown{}
	if err := d.Get(ctx, client.ObjectKey{Name: rig.Spec.TownRef}, gastown); err != nil {
		if apierrors.IsNotFound(err) {
			// GasTown not yet created; allow through without defaulting.
			// The validating webhook (Rule 5) will deny on the same request.
			logger.Info("parent GasTown not found during defaulting, skipping",
				"townRef", rig.Spec.TownRef)
			return admission.Allowed("")
		}
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("get gastown %q: %w", rig.Spec.TownRef, err))
	}

	defaults := gastown.Spec.Defaults
	mutated := false

	// Mutation 1: mayorModel default.
	if rig.Spec.Agents.MayorModel == "" && defaults.MayorModel != "" {
		rig.Spec.Agents.MayorModel = defaults.MayorModel
		mutated = true
	}

	// Mutation 2: polecatModel default.
	if rig.Spec.Agents.PolecatModel == "" && defaults.PolecatModel != "" {
		rig.Spec.Agents.PolecatModel = defaults.PolecatModel
		mutated = true
	}

	// Mutation 3: maxPolecats default.
	if rig.Spec.Agents.MaxPolecats == 0 && defaults.MaxPolecats != 0 {
		rig.Spec.Agents.MaxPolecats = defaults.MaxPolecats
		mutated = true
	}

	if !mutated {
		return admission.Allowed("")
	}

	marshaled, err := json.Marshal(rig)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled)
}

// Ensure RigDefaulter implements the handler interface.
var _ admission.Handler = &RigDefaulter{}
