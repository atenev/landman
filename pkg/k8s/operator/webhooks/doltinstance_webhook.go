package webhooks

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
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

// Handle validates a DoltInstance admission request.
// Rules enforced:
//   - Rule 8: replicas must be 1 (MVP constraint, ADR-0005 D4).
//   - Immutable fields on UPDATE: spec.storage.storageClassName, spec.storage.size,
//     and spec.version (downgrade only — upgrades are permitted).
func (v *DoltInstanceValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	di := &gasv1alpha1.DoltInstance{}
	if err := v.decoder.DecodeRaw(req.Object, di); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Rule 8: replicas must be 1 (MVP constraint, ADR-0005 D4).
	if di.Spec.Replicas != 1 {
		return admission.Denied(fmt.Sprintf(
			"spec.replicas: Invalid value: %d: replicas must be 1 in "+
				"gastown-operator v0.x", di.Spec.Replicas))
	}

	// Immutable field enforcement on UPDATE.
	if req.Operation == admissionv1.Update {
		old := &gasv1alpha1.DoltInstance{}
		if err := v.decoder.DecodeRaw(req.OldObject, old); err != nil {
			return admission.Errored(http.StatusBadRequest,
				fmt.Errorf("decode oldObject: %w", err))
		}

		// spec.storage.storageClassName is immutable: the StatefulSet
		// VolumeClaimTemplate cannot be changed post-creation.
		if di.Spec.Storage.StorageClassName != old.Spec.Storage.StorageClassName {
			return admission.Denied(fmt.Sprintf(
				"spec.storage.storageClassName: Forbidden: field is immutable after creation "+
					"(old: %q, new: %q)",
				old.Spec.Storage.StorageClassName, di.Spec.Storage.StorageClassName))
		}

		// spec.storage.size is immutable: the operator does not reconcile PVC
		// capacity changes, so updates would be silently ignored.
		if di.Spec.Storage.Size.Cmp(old.Spec.Storage.Size) != 0 {
			return admission.Denied(fmt.Sprintf(
				"spec.storage.size: Forbidden: field is immutable after creation "+
					"(old: %q, new: %q)",
				old.Spec.Storage.Size.String(), di.Spec.Storage.Size.String()))
		}

		// spec.version downgrade is forbidden: schema migrations run
		// forward-only and cannot be reversed.
		if isVersionDowngrade(old.Spec.Version, di.Spec.Version) {
			return admission.Denied(fmt.Sprintf(
				"spec.version: Forbidden: version downgrade not allowed "+
					"(old: %q, new: %q)",
				old.Spec.Version, di.Spec.Version))
		}
	}

	return admission.Allowed("")
}

// isVersionDowngrade reports whether newVer is an older release than oldVer.
// Versions are expected to follow the vMAJOR.MINOR.PATCH format. If either
// version cannot be parsed the function returns false (allow through).
func isVersionDowngrade(oldVer, newVer string) bool {
	old := parseSemver(oldVer)
	nw := parseSemver(newVer)
	if old == nil || nw == nil {
		return false
	}
	for i := range old {
		if nw[i] < old[i] {
			return true
		}
		if nw[i] > old[i] {
			return false
		}
	}
	return false
}

// parseSemver parses a vMAJOR.MINOR.PATCH string into a three-element slice.
// Returns nil when the input does not match the expected format.
func parseSemver(v string) []int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		nums[i] = n
	}
	return nums
}

// Ensure DoltInstanceValidator implements the handler interface.
var _ admission.Handler = &DoltInstanceValidator{}
