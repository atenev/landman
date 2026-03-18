// Package v1alpha1 — Gas Town operator CRD type definitions.
//
// Four CRDs are defined here:
//   - GasTown (cluster-scoped): top-level Gas Town instance
//   - Rig (namespaced):         a single git repository + agent pool
//   - AgentRole (namespaced):   a custom agent archetype
//   - DoltInstance (namespaced): the Dolt SQL backing store
//
// kubebuilder markers on each type drive CRD YAML generation (see doc.go).
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// SchemeGroupVersion is the group and version for all Gas Town CRDs.
var SchemeGroupVersion = schema.GroupVersion{Group: "gastown.tenev.io", Version: "v1alpha1"}

// SchemeBuilder registers the CRD types with a Kubernetes runtime scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds the CRD types to the given scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&GasTown{},
		&GasTownList{},
		&Rig{},
		&RigList{},
		&AgentRole{},
		&AgentRoleList{},
		&DoltInstance{},
		&DoltInstanceList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}

// ─── Shared types ─────────────────────────────────────────────────────────────

// LocalRef names a Kubernetes object in the same namespace as the referencing CR.
type LocalRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// NamespacedRef names a Kubernetes object in a specific namespace.
type NamespacedRef struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
}

// ─── GasTown (cluster-scoped) ─────────────────────────────────────────────────

// GasTown is the top-level cluster-scoped resource representing a Gas Town
// instance. One GasTown CR per deployment environment (dev / staging / prod).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=gt
type GasTown struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GasTownSpec   `json:"spec,omitempty"`
	Status GasTownStatus `json:"status,omitempty"`
}

// GasTownSpec defines the desired state of a Gas Town instance.
type GasTownSpec struct {
	// Version is the town.toml schema version this CR corresponds to.
	// Must match a version known to town-ctl and the operator.
	// +kubebuilder:validation:Pattern=`^\d+$`
	Version string `json:"version"`

	// Home is the base directory for Gas Town data on agent hosts.
	// +kubebuilder:validation:MinLength=1
	Home string `json:"home"`

	// DoltRef references the DoltInstance backing this town.
	DoltRef NamespacedRef `json:"doltRef"`

	// Defaults provides town-wide agent configuration defaults.
	// Individual Rig CRs may override these per-field.
	// +optional
	Defaults GasTownDefaults `json:"defaults,omitempty"`

	// Agents configures town-level agent process lifecycle.
	// +optional
	Agents TownAgents `json:"agents,omitempty"`

	// SecretsRef names a Kubernetes Secret containing API keys and tokens.
	// Values are injected as env vars into agent pods — never written to Dolt.
	// +optional
	SecretsRef *LocalRef `json:"secretsRef,omitempty"`
}

// GasTownDefaults provides town-wide defaults inherited by all Rig CRs.
type GasTownDefaults struct {
	// MayorModel is the default Claude model for Mayor agents.
	// +optional
	// +kubebuilder:default="claude-opus-4-6"
	MayorModel string `json:"mayorModel,omitempty"`

	// PolecatModel is the default Claude model for Polecat agents.
	// +optional
	// +kubebuilder:default="claude-sonnet-4-6"
	PolecatModel string `json:"polecatModel,omitempty"`

	// MaxPolecats is the default maximum concurrent Polecat agents per rig.
	// +optional
	// +kubebuilder:default=20
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxPolecats int32 `json:"maxPolecats,omitempty"`
}

// TownAgents configures town-level agents managed by the operator.
type TownAgents struct {
	// Surveyor controls whether the operator manages a Surveyor Deployment.
	// +optional
	// +kubebuilder:default=false
	Surveyor bool `json:"surveyor,omitempty"`

	// SurveyorClaudeMdRef names a ConfigMap containing the Surveyor CLAUDE.md.
	// Required when Surveyor=true.
	// +optional
	SurveyorClaudeMdRef *LocalRef `json:"surveyorClaudeMdRef,omitempty"`

	// SurveyorImage overrides the default Surveyor container image.
	// +optional
	SurveyorImage string `json:"surveyorImage,omitempty"`

	// SurveyorResources sets resource requests/limits for the Surveyor pod.
	// +optional
	SurveyorResources *corev1.ResourceRequirements `json:"surveyorResources,omitempty"`
}

// GasTownStatus describes the observed state of a GasTown instance.
type GasTownStatus struct {
	// Conditions reflect the current state of the GasTown (Ready,
	// SurveyorRunning, DesiredTopologyInSync).
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DoltCommit is the Dolt commit hash of the last successful desired_topology write.
	// +optional
	DoltCommit string `json:"doltCommit,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastReconcileAt is when the Surveyor last completed a reconcile loop
	// (read from actual_topology).
	// +optional
	LastReconcileAt *metav1.Time `json:"lastReconcileAt,omitempty"`
}

// GasTownList contains a list of GasTown CRs.
//
// +kubebuilder:object:root=true
type GasTownList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GasTown `json:"items"`
}

// ─── Rig (namespaced) ──────────────────────────────────────────────────────────

// Rig represents a single Gas Town rig: one git repository and its agent pool.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rig
type Rig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RigSpec   `json:"spec,omitempty"`
	Status RigStatus `json:"status,omitempty"`
}

// RigSpec defines the desired state of a Rig.
type RigSpec struct {
	// TownRef names the GasTown CR this Rig belongs to.
	// +kubebuilder:validation:MinLength=1
	TownRef string `json:"townRef"`

	// Repo is the filesystem path to the git repository for this rig.
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Branch is the git branch this rig tracks.
	// +kubebuilder:default="main"
	Branch string `json:"branch"`

	// Enabled controls whether this rig is active. Disabled rigs are drained
	// and stopped by the Surveyor.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Agents configures the agent roles active for this rig.
	// +optional
	Agents RigAgents `json:"agents,omitempty"`

	// Formulas lists cron-driven Formula schedules active for this rig.
	// +optional
	Formulas []RigFormula `json:"formulas,omitempty"`

	// Roles lists AgentRole names (in this namespace) opted into by this rig.
	// Only AgentRole objects with scope=rig require opt-in here.
	// +optional
	Roles []string `json:"roles,omitempty"`
}

// RigAgents configures which built-in agent roles are active and their overrides.
type RigAgents struct {
	// Mayor enables the Mayor agent for this rig.
	// +optional
	// +kubebuilder:default=true
	Mayor bool `json:"mayor,omitempty"`

	// Witness enables the Witness agent for this rig.
	// +optional
	// +kubebuilder:default=true
	Witness bool `json:"witness,omitempty"`

	// Refinery enables the Refinery agent for this rig.
	// +optional
	// +kubebuilder:default=true
	Refinery bool `json:"refinery,omitempty"`

	// Deacon enables the Deacon agent for this rig.
	// +optional
	// +kubebuilder:default=true
	Deacon bool `json:"deacon,omitempty"`

	// MayorModel overrides GasTown.spec.defaults.mayorModel for this rig.
	// +optional
	MayorModel string `json:"mayorModel,omitempty"`

	// PolecatModel overrides GasTown.spec.defaults.polecatModel for this rig.
	// +optional
	PolecatModel string `json:"polecatModel,omitempty"`

	// MaxPolecats overrides GasTown.spec.defaults.maxPolecats for this rig.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxPolecats int32 `json:"maxPolecats,omitempty"`

	// MayorClaudeMdRef names a ConfigMap containing the Mayor's CLAUDE.md for this rig.
	// +optional
	MayorClaudeMdRef *LocalRef `json:"mayorClaudeMdRef,omitempty"`
}

// RigFormula declares a cron-driven Formula workflow for a rig.
type RigFormula struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Schedule is a standard 5-field cron expression.
	// +kubebuilder:validation:Pattern=`^(@(annually|yearly|monthly|weekly|daily|hourly|reboot))|(((\d+,)+\d+|(\d+(\/|-)\d+)|\d+|\*) ){4}((\d+,)+\d+|(\d+(\/|-)\d+)|\d+|\*)$`
	Schedule string `json:"schedule"`
}

// RigStatus describes the observed state of a Rig.
type RigStatus struct {
	// Conditions reflect the current state of the Rig (Ready, DesiredTopologyInSync).
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DoltCommit is the Dolt commit hash of the last successful desired_topology write.
	// +optional
	DoltCommit string `json:"doltCommit,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// RunningPolecats is the current number of running Polecat agents
	// (read from actual_topology).
	// +optional
	RunningPolecats int32 `json:"runningPolecats,omitempty"`

	// RigHealth is the health summary from actual_topology: healthy, degraded, or unknown.
	// +optional
	RigHealth string `json:"rigHealth,omitempty"`

	// LastConvergedAt is when the rig last reached a fully converged state.
	// +optional
	LastConvergedAt *metav1.Time `json:"lastConvergedAt,omitempty"`
}

// RigList contains a list of Rig CRs.
//
// +kubebuilder:object:root=true
type RigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Rig `json:"items"`
}

// ─── AgentRole (namespaced) ────────────────────────────────────────────────────

// AgentRole declares a custom agent archetype that can be activated per rig
// (scope=rig) or town-wide (scope=town).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ar
type AgentRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRoleSpec   `json:"spec,omitempty"`
	Status AgentRoleStatus `json:"status,omitempty"`
}

// AgentRoleSpec defines the desired state of an AgentRole.
type AgentRoleSpec struct {
	// TownRef names the GasTown CR this role belongs to.
	// +kubebuilder:validation:MinLength=1
	TownRef string `json:"townRef"`

	// Scope determines whether this role is activated per-rig or town-wide.
	// +kubebuilder:validation:Enum=rig;town
	Scope string `json:"scope"`

	// Identity declares the agent's CLAUDE.md file location.
	Identity AgentRoleIdentity `json:"identity"`

	// Trigger defines how the role is activated.
	Trigger AgentRoleTrigger `json:"trigger"`

	// Supervision declares this role's position in the agent hierarchy.
	// Parent is required — no role may exist without a supervisor.
	Supervision AgentRoleSupervision `json:"supervision"`

	// Resources constrains instance count for this role.
	// +optional
	Resources AgentRoleResources `json:"resources,omitempty"`
}

// AgentRoleIdentity specifies how the role presents itself as a Claude Code agent.
type AgentRoleIdentity struct {
	// ClaudeMdRef names a ConfigMap containing the CLAUDE.md for this role.
	// Inline content is not supported (ADR-0004, Decision 2).
	ClaudeMdRef LocalRef `json:"claudeMdRef"`
}

// AgentRoleTrigger defines when a custom role agent is spawned.
type AgentRoleTrigger struct {
	// Type determines the activation mechanism.
	// +kubebuilder:validation:Enum=bead_assigned;schedule;event;manual
	Type string `json:"type"`

	// Schedule is a cron expression. Required when Type=schedule.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Event is the event name polled via bd search. Required when Type=event.
	// +optional
	Event string `json:"event,omitempty"`
}

// AgentRoleSupervision declares where this role sits in the agent hierarchy.
type AgentRoleSupervision struct {
	// Parent is the built-in or custom role that supervises this role.
	// Must be non-empty. Webhook enforces that it does not equal this role's name.
	// +kubebuilder:validation:MinLength=1
	Parent string `json:"parent"`

	// ReportsTo is the escalation target. Defaults to Parent if empty.
	// +optional
	ReportsTo string `json:"reportsTo,omitempty"`
}

// AgentRoleResources constrains the number of instances that can run simultaneously.
type AgentRoleResources struct {
	// MaxInstances sets the capacity ceiling for concurrent instances.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	MaxInstances int32 `json:"maxInstances,omitempty"`
}

// AgentRoleStatus describes the observed state of an AgentRole.
type AgentRoleStatus struct {
	// Conditions reflect the current state of the AgentRole (Ready, DesiredTopologyInSync).
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DoltCommit is the Dolt commit hash of the last successful desired_custom_roles write.
	// +optional
	DoltCommit string `json:"doltCommit,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ActiveInstances is the current number of running instances of this role
	// (read from actual_topology).
	// +optional
	ActiveInstances int32 `json:"activeInstances,omitempty"`

	// LastSeenAt is when an instance of this role was last observed active.
	// +optional
	LastSeenAt *metav1.Time `json:"lastSeenAt,omitempty"`
}

// AgentRoleList contains a list of AgentRole CRs.
//
// +kubebuilder:object:root=true
type AgentRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRole `json:"items"`
}

// ─── DoltInstance (namespaced) ─────────────────────────────────────────────────

// DoltInstance manages a single Dolt SQL server backing a Gas Town instance.
// The operator reconciles a StatefulSet, a headless Service, and a PVC.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=dolt
type DoltInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoltInstanceSpec   `json:"spec,omitempty"`
	Status DoltInstanceStatus `json:"status,omitempty"`
}

// DoltInstanceSpec defines the desired state of a DoltInstance.
type DoltInstanceSpec struct {
	// Version is the Dolt container image tag to use.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Replicas must be 1 in MVP. Field is present for API stability only.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	Replicas int32 `json:"replicas"`

	// Storage configures the PersistentVolumeClaim for Dolt data.
	Storage DoltStorage `json:"storage"`

	// Service configures the Kubernetes Service for Dolt.
	// +optional
	Service DoltService `json:"service,omitempty"`
}

// DoltStorage configures the PVC for Dolt data.
type DoltStorage struct {
	// StorageClassName names the StorageClass to use for the PVC.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// Size is the requested PVC capacity (e.g. "10Gi").
	// resource.Quantity has its own OpenAPI schema type; no extra Pattern needed.
	Size resource.Quantity `json:"size"`
}

// DoltService configures the Kubernetes Service for Dolt.
type DoltService struct {
	// Type is the Kubernetes Service type.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	Type string `json:"type,omitempty"`

	// Port is the port on which the Service exposes Dolt's MySQL-compatible endpoint.
	// +kubebuilder:default=3306
	Port int32 `json:"port,omitempty"`
}

// DoltInstanceStatus describes the observed state of a DoltInstance.
type DoltInstanceStatus struct {
	// Conditions reflect the current state of the DoltInstance
	// (Ready, DDLInitialized, VersionMigrationRequired).
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// CurrentVersion is the Dolt image tag currently running.
	// +optional
	CurrentVersion string `json:"currentVersion,omitempty"`

	// Endpoint is the in-cluster DNS address of the Dolt service,
	// e.g. "dolt-instance.my-namespace.svc.cluster.local:3306".
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
}

// DoltInstanceList contains a list of DoltInstance CRs.
//
// +kubebuilder:object:root=true
type DoltInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoltInstance `json:"items"`
}
