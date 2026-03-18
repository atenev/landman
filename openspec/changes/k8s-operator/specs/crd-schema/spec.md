# Spec: CRD Schema — GasTown, Rig, AgentRole, DoltInstance

## Purpose

Define the Go type definitions (`types.go`) and OpenAPI v3 validation schemas for all four
Gas Town CRDs. These types are the source of truth for CRD generation via controller-gen.

## GasTown (cluster-scoped)

```go
// GasTownSpec defines the desired state of a Gas Town instance.
type GasTownSpec struct {
    // Version is the town.toml schema version this CR corresponds to.
    // Must match a version known to town-ctl and the operator. Required.
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

type GasTownStatus struct {
    // Conditions: Ready, SurveyorRunning, DesiredTopologyInSync
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
```

## Rig (namespaced)

```go
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

    // Roles lists AgentRole names (defined in this namespace) opted into by this rig.
    // +optional
    Roles []string `json:"roles,omitempty"`
}

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
    // +optional
    MayorClaudeMdRef *LocalRef `json:"mayorClaudeMdRef,omitempty"`
}

type RigFormula struct {
    // +kubebuilder:validation:MinLength=1
    Name string `json:"name"`
    // +kubebuilder:validation:Pattern=`^(@(annually|yearly|monthly|weekly|daily|hourly|reboot))|(((\d+,)+\d+|(\d+(\/|-)\d+)|\d+|\*) ){4}((\d+,)+\d+|(\d+(\/|-)\d+)|\d+|\*)$`
    Schedule string `json:"schedule"`
}

type RigStatus struct {
    Conditions         []metav1.Condition `json:"conditions,omitempty"`
    DoltCommit         string             `json:"doltCommit,omitempty"`
    ObservedGeneration int64              `json:"observedGeneration,omitempty"`
    // From actual_topology (populated by status sync loop):
    RunningPolecats    int32              `json:"runningPolecats,omitempty"`
    RigHealth          string             `json:"rigHealth,omitempty"` // healthy|degraded|unknown
    LastConvergedAt    *metav1.Time       `json:"lastConvergedAt,omitempty"`
}
```

## AgentRole (namespaced)

```go
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

type AgentRoleIdentity struct {
    // ClaudeMdRef names a ConfigMap containing the CLAUDE.md for this role.
    // Inline content is not supported (ADR-0004 D2).
    ClaudeMdRef LocalRef `json:"claudeMdRef"`
}

type AgentRoleTrigger struct {
    // +kubebuilder:validation:Enum=bead_assigned;schedule;event;manual
    Type string `json:"type"`
    // Required when Type=schedule. Cron expression.
    // +optional
    Schedule string `json:"schedule,omitempty"`
    // Required when Type=event. Event name polled via bd search.
    // +optional
    Event string `json:"event,omitempty"`
}

type AgentRoleSupervision struct {
    // Parent is the built-in or custom role that supervises this role.
    // Must be non-empty and must not equal this role's name.
    // +kubebuilder:validation:MinLength=1
    Parent string `json:"parent"`
    // ReportsTo is the escalation target for this role. Optional.
    // +optional
    ReportsTo string `json:"reportsTo,omitempty"`
}

type AgentRoleResources struct {
    // +kubebuilder:default=1
    // +kubebuilder:validation:Minimum=1
    MaxInstances int32 `json:"maxInstances,omitempty"`
}

type AgentRoleStatus struct {
    Conditions         []metav1.Condition `json:"conditions,omitempty"`
    DoltCommit         string             `json:"doltCommit,omitempty"`
    ObservedGeneration int64              `json:"observedGeneration,omitempty"`
    ActiveInstances    int32              `json:"activeInstances,omitempty"`
    LastSeenAt         *metav1.Time       `json:"lastSeenAt,omitempty"`
}
```

## DoltInstance (namespaced)

```go
type DoltInstanceSpec struct {
    // Version is the Dolt container image tag to use.
    // +kubebuilder:validation:MinLength=1
    Version string `json:"version"`

    // Replicas must be 1 in MVP. Field exists for API stability.
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

type DoltStorage struct {
    // +optional
    StorageClassName string            `json:"storageClassName,omitempty"`
    // +kubebuilder:validation:Pattern=`^\d+(Ki|Mi|Gi|Ti|Pi|Ei)?$`
    Size             resource.Quantity `json:"size"`
}

type DoltService struct {
    // +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
    // +kubebuilder:default=ClusterIP
    Type string `json:"type,omitempty"`
    // +kubebuilder:default=3306
    Port int32  `json:"port,omitempty"`
}

type DoltInstanceStatus struct {
    Conditions         []metav1.Condition `json:"conditions,omitempty"`
    ObservedGeneration int64              `json:"observedGeneration,omitempty"`
    CurrentVersion     string             `json:"currentVersion,omitempty"`
    // Endpoint is the in-cluster DNS address of the Dolt service.
    Endpoint           string             `json:"endpoint,omitempty"`
}
```

## Shared Types

```go
// LocalRef names a Kubernetes object in the same namespace.
type LocalRef struct {
    Name string `json:"name"`
}

// NamespacedRef names a Kubernetes object in a specific namespace.
type NamespacedRef struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
}
```

## Validation Constraints Summary

| Field | Constraint | Source |
|-------|-----------|--------|
| `AgentRole.metadata.name` | Not in reserved list (webhook) | ADR-0004 D4 |
| `AgentRole.spec.supervision.parent` | MinLength=1 (schema) + not self (webhook) | ADR-0004 D3 |
| `AgentRole.spec.trigger.schedule` | Required when type=schedule (webhook) | ADR-0004 D6 |
| `AgentRole.spec.trigger.event` | Required when type=event (webhook) | ADR-0004 D6 |
| `DoltInstance.spec.replicas` | Maximum=1 (schema, MVP) | ADR-0005 D4 |
| `Rig.spec.townRef` | Resolves to existing GasTown (webhook) | ADR-0005 D2 |
| `GasTown.spec.doltRef` | Resolves to existing DoltInstance (webhook) | ADR-0005 D2 |
| `RigFormula.schedule` | Cron pattern validation (schema) | — |
