// Package v1alpha1 contains the API type definitions for the Gas Town
// Kubernetes operator. These types are the source of truth for CRD generation
// via controller-gen. All four CRDs share the group gastown.tenev.io and
// version v1alpha1.
//
// To regenerate CRD YAML and DeepCopy functions, run:
//
//	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./pkg/k8s/operator/v1alpha1/..."
//	controller-gen crd paths="./pkg/k8s/operator/v1alpha1/..." output:crd:artifacts:config=config/crd/bases

// +groupName=gastown.tenev.io
// +kubebuilder:object:generate=true
package v1alpha1
