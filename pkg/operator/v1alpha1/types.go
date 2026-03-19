// Package v1alpha1 defines the Go types for Gas Town Kubernetes CRDs
// (ADR-0005, dgt-3j8). These types are the source of truth for CRD generation
// via controller-gen (see openspec/changes/k8s-operator/specs/crd-schema/spec.md).
//
// Build note: this package imports k8s.io/apimachinery and controller-runtime
// which are not yet in go.mod. Add them when scaffolding the operator binary
// (dgt-3xu). Until then this file uses build constraints to avoid breaking the
// current build.
//
//go:build ignore

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ──────────────────────────────────────────────────────────────────────────────
// Shared types
// ──────────────────────────────────────────────────────────────────────────────

// LocalRef names a Kubernetes object in the same namespace.
type LocalRef struct {
	Name string `json:"name"`
}

// NamespacedRef names a Kubernetes object in a specific namespace.
type NamespacedRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ──────────────────────────────────────────────────────────────────────────────
// GasTown (cluster-scoped)
// ──────────────────────────────────────────────────────────────────────────────

// GasTown is the cluster-scoped root CR for a Gas Town instance. It references a
// DoltInstance (the backing Dolt database) and provides town-wide defaults that
// propagate to all Rig CRs in the same deployment.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Dolt",type=string,JSONPath=`.spec.doltRef.name`
// +kubebuilder:printcolumn:name="Surveyor",type=boolean,JSONPath=`.spec.agents.surveyor`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type GasTown struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GasTownSpec   `json:"spec,omitempty"`
	Status GasTownStatus `json:"status,omitempty"`
}

// GasTownSpec defines the desired state of a Gas Town instance.
type GasTownSpec struct {
	// Version is the town.toml schema version this CR corresponds to. Must match a
	// version known to the operator. Required.
	// +kubebuilder:validation:Pattern=`^\d+$`
	Version string `json:"version"`

	// Home is the base directory for Gas Town data on agent hosts.
	// +kubebuilder:validation:MinLength=1
	Home string `json:"home"`

	// DoltRef references the DoltInstance backing this town.
	DoltRef NamespacedRef `json:"doltRef"`

	// Defaults provides town-wide agent configuration defaults inherited by all
	// Rig CRs unless overridden per-field.
	// +optional
	Defaults GasTownDefaults `json:"defaults,omitempty"`

	// Agents configures town-level agent process lifecycle.
	// +optional
	Agents TownAgents `json:"agents,omitempty"`

	// SecretsRef names a Kubernetes Secret containing API keys and tokens. Values
	// are injected as env vars into agent pods — never written to Dolt (ADR-0001 D4).
	// +optional
	SecretsRef *LocalRef `json:"secretsRef,omitempty"`
}

// GasTownDefaults holds town-wide agent configuration defaults.
type GasTownDefaults struct {
	// +optional
	// +kubebuilder:default="claude-opus-4-6"
	MayorModel string `json:"mayorModel,omitempty"`

	// +optional
	// +kubebuilder:default="claude-sonnet-4-6"
	PolecatModel string `json:"polecatModel,omitempty"`

	// +optional
	// +kubebuilder:default=20
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	MaxPolecats int32 `json:"maxPolecats,omitempty"`
}

// TownAgents configures which Gas Town-level agents the operator manages.
type TownAgents struct {
	// Surveyor controls whether the operator creates and manages a Surveyor
	// Deployment for this town. Requires SurveyorClaudeMdRef when true.
	// +optional
	// +kubebuilder:default=false
	Surveyor bool `json:"surveyor,omitempty"`

	// SurveyorClaudeMdRef names a ConfigMap containing the Surveyor's CLAUDE.md.
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

// GasTownStatus reflects the observed state of a Gas Town instance.
type GasTownStatus struct {
	// Conditions: Ready, SurveyorRunning, DesiredTopologyInSync.
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DoltCommit is the Dolt commit hash of the last successful desired_topology write.
	// +optional
	DoltCommit string `json:"doltCommit,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastReconcileAt is when the Surveyor last completed a reconcile loop (from actual_topology).
	// +optional
	LastReconcileAt *metav1.Time `json:"lastReconcileAt,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Rig (namespaced)
// ──────────────────────────────────────────────────────────────────────────────

// Rig represents one Git repository managed by a Gas Town instance. The operator
// writes desired_rigs, desired_agent_config, desired_formulas, and
// desired_rig_custom_roles rows to Dolt when a Rig CR is applied.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Town",type=string,JSONPath=`.spec.townRef`
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repo`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="Polecats",type=integer,JSONPath=`.status.runningPolecats`
// +kubebuilder:printcolumn:name="Health",type=string,JSONPath=`.status.rigHealth`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Rig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RigSpec   `json:"spec,omitempty"`
	Status RigStatus `json:"status,omitempty"`
}

// RigSpec defines the desired state of a single Gas Town rig.
type RigSpec struct {
	// TownRef names the GasTown CR this Rig belongs to. Must match a GasTown
	// name in the cluster (validated by admission webhook).
	// +kubebuilder:validation:MinLength=1
	TownRef string `json:"townRef"`

	// Repo is the filesystem path to the git repository.
	// +kubebuilder:validation:MinLength=1
	Repo string `json:"repo"`

	// Branch is the git branch this rig tracks.
	// +kubebuilder:default="main"
	Branch string `json:"branch"`

	// Enabled controls whether this rig is active. Disabled rigs are drained and
	// stopped by the Surveyor via the desired_rigs.enabled column.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Agents configures the agent roles active for this rig.
	// +optional
	Agents RigAgents `json:"agents,omitempty"`

	// Formulas lists cron-driven Formula schedules active for this rig.
	// +optional
	Formulas []RigFormula `json:"formulas,omitempty"`

	// Roles lists AgentRole object names (in this namespace) opted into by this rig.
	// +optional
	Roles []string `json:"roles,omitempty"`
}

// RigAgents specifies which agent roles are active for a rig and their per-rig overrides.
type RigAgents struct {
	// +optional
	// +kubebuilder:default=true
	Mayor bool `json:"mayor,omitempty"`

	// +optional
	// +kubebuilder:default=true
	Witness bool `json:"witness,omitempty"`

	// +optional
	// +kubebuilder:default=true
	Refinery bool `json:"refinery,omitempty"`

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
	// Overrides the default Mayor identity for this rig only.
	// +optional
	MayorClaudeMdRef *LocalRef `json:"mayorClaudeMdRef,omitempty"`
}

// RigFormula declares a cron-driven Formula workflow for a rig.
type RigFormula struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// +kubebuilder:validation:Pattern=`^(@(annually|yearly|monthly|weekly|daily|hourly|reboot))|(((\d+,)+\d+|(\d+(\/|-)\d+)|\d+|\*) ){4}((\d+,)+\d+|(\d+(\/|-)\d+)|\d+|\*)$`
	Schedule string `json:"schedule"`
}

// RigStatus reflects the observed state of a rig.
type RigStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	DoltCommit         string             `json:"doltCommit,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	// RunningPolecats is populated by the status-sync loop from actual_rigs.
	RunningPolecats int32  `json:"runningPolecats,omitempty"`
	// RigHealth reflects actual_rigs.health: "healthy", "degraded", or "unknown".
	RigHealth       string `json:"rigHealth,omitempty"`
	// LastConvergedAt is when actual_topology last matched desired_topology for this rig.
	LastConvergedAt *metav1.Time `json:"lastConvergedAt,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// AgentRole (namespaced)
// ──────────────────────────────────────────────────────────────────────────────

// AgentRole declares a custom Gas Town agent role (ADR-0004). The operator writes
// desired_custom_roles rows and, when Rig CRs reference this role,
// desired_rig_custom_roles rows.
//
// Admission webhook enforces:
//   - metadata.name not in reserved built-in role list (ADR-0004 D4)
//   - spec.supervision.parent non-empty and not self-referential (ADR-0004 D3)
//   - trigger cross-field rules (ADR-0004 D6)
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.spec.scope`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.trigger.type`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeInstances`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type AgentRole struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRoleSpec   `json:"spec,omitempty"`
	Status AgentRoleStatus `json:"status,omitempty"`
}

// AgentRoleSpec defines the desired state of a custom agent role.
type AgentRoleSpec struct {
	// TownRef names the GasTown CR this role belongs to.
	// +kubebuilder:validation:MinLength=1
	TownRef string `json:"townRef"`

	// Scope determines activation: rig (per-rig opt-in via Rig.spec.roles) or
	// town (active on every rig automatically).
	// +kubebuilder:validation:Enum=rig;town
	Scope string `json:"scope"`

	// Identity declares how this role presents itself as a Claude Code agent.
	Identity AgentRoleIdentity `json:"identity"`

	// Trigger defines how the role is activated.
	Trigger AgentRoleTrigger `json:"trigger"`

	// Supervision declares this role's position in the agent hierarchy.
	// Parent is required — every role must have a supervisor (ADR-0004 D3).
	Supervision AgentRoleSupervision `json:"supervision"`

	// Resources constrains the number of concurrent instances.
	// +optional
	Resources AgentRoleResources `json:"resources,omitempty"`
}

// AgentRoleIdentity specifies how the role presents itself as a Claude Code agent.
type AgentRoleIdentity struct {
	// ClaudeMdRef names a ConfigMap containing this role's CLAUDE.md. Inline content
	// is not supported — a ConfigMap reference is required (ADR-0004 D2).
	ClaudeMdRef LocalRef `json:"claudeMdRef"`

	// Model overrides the Claude model for this role. Inherits from rig defaults if empty.
	// +optional
	Model string `json:"model,omitempty"`
}

// AgentRoleTrigger defines when a custom role agent is spawned.
type AgentRoleTrigger struct {
	// +kubebuilder:validation:Enum=bead_assigned;schedule;event;manual
	Type string `json:"type"`

	// Schedule is required when Type=schedule. 5-field cron expression.
	// +optional
	Schedule string `json:"schedule,omitempty"`

	// Event is required when Type=event. Event name polled via Beads.
	// +optional
	Event string `json:"event,omitempty"`
}

// AgentRoleSupervision declares where this role sits in the Gas Town agent hierarchy.
type AgentRoleSupervision struct {
	// Parent is the built-in or custom role that supervises this role. Required.
	// Must not be empty and must not equal this role's own name.
	// +kubebuilder:validation:MinLength=1
	Parent string `json:"parent"`

	// ReportsTo is the escalation target. Defaults to Parent if omitted.
	// +optional
	ReportsTo string `json:"reportsTo,omitempty"`
}

// AgentRoleResources constrains how many instances may run simultaneously.
type AgentRoleResources struct {
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	MaxInstances int32 `json:"maxInstances,omitempty"`
}

// AgentRoleStatus reflects the observed state of a custom agent role.
type AgentRoleStatus struct {
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	DoltCommit         string             `json:"doltCommit,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	// ActiveInstances is populated by the status-sync loop from actual_custom_roles.
	ActiveInstances int32        `json:"activeInstances,omitempty"`
	LastSeenAt      *metav1.Time `json:"lastSeenAt,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// DoltInstance (namespaced)
// ──────────────────────────────────────────────────────────────────────────────

// DoltInstance declares a managed Dolt database for a Gas Town deployment. The
// operator creates a StatefulSet, headless Service, ClusterIP Service, and PVC.
// On first startup it runs a DDL init Job to create desired_topology tables.
// On spec.version change it runs a schema migration Job (ADR-0003).
//
// MVP constraint: spec.replicas must be 1. The admission webhook rejects values > 1.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type DoltInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoltInstanceSpec   `json:"spec,omitempty"`
	Status DoltInstanceStatus `json:"status,omitempty"`
}

// DoltInstanceSpec defines the desired state of a managed Dolt database.
type DoltInstanceSpec struct {
	// Version is the Dolt container image tag (e.g. "1.42.0").
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Replicas must be 1 in MVP. Field exists for API stability; webhook rejects > 1.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	Replicas int32 `json:"replicas"`

	// Storage configures the PersistentVolumeClaim for Dolt data.
	Storage DoltStorage `json:"storage"`

	// Service configures the Kubernetes Service exposing Dolt.
	// +optional
	Service DoltService `json:"service,omitempty"`
}

// DoltStorage configures Dolt's PersistentVolumeClaim.
type DoltStorage struct {
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// Size is the PVC storage request (e.g. "50Gi").
	// +kubebuilder:validation:Pattern=`^\d+(Ki|Mi|Gi|Ti|Pi|Ei)?$`
	Size resource.Quantity `json:"size"`
}

// DoltService configures the Kubernetes Service for the Dolt StatefulSet.
type DoltService struct {
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	Type string `json:"type,omitempty"`

	// +kubebuilder:default=3306
	Port int32 `json:"port,omitempty"`
}

// DoltInstanceStatus reflects the observed state of a managed Dolt database.
type DoltInstanceStatus struct {
	// Conditions: Ready, DDLInitialized, VersionMigrationRequired.
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	// CurrentVersion is the Dolt image tag currently running in the StatefulSet.
	CurrentVersion string `json:"currentVersion,omitempty"`
	// Endpoint is the in-cluster DNS address of the Dolt service: "<name>.<ns>.svc.cluster.local:<port>".
	Endpoint string `json:"endpoint,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Registration helpers (generated by controller-gen in dgt-3xu)
// ──────────────────────────────────────────────────────────────────────────────

// GasTownList is a list of GasTown objects (required for controller-runtime).
// +kubebuilder:object:root=true
type GasTownList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GasTown `json:"items"`
}

// RigList is a list of Rig objects.
// +kubebuilder:object:root=true
type RigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Rig `json:"items"`
}

// AgentRoleList is a list of AgentRole objects.
// +kubebuilder:object:root=true
type AgentRoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRole `json:"items"`
}

// DoltInstanceList is a list of DoltInstance objects.
// +kubebuilder:object:root=true
type DoltInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoltInstance `json:"items"`
}
