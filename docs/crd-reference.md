# Kubernetes CRD Reference

Reference for the four Custom Resource Definitions (CRDs) provided by the
`gastown-operator`.

> **See also**: [Declarative Overview](declarative-overview.md) for conceptual
> background; [docs/nix-module.md](nix-module.md) for the NixOS module reference.

---

## Overview

| CRD | API Group | Scope | Short Name | Purpose |
|-----|-----------|-------|------------|---------|
| [`GasTown`](#gastown) | `gastown.tenev.io/v1alpha1` | Cluster | `gt` | Town metadata, Surveyor Deployment |
| [`Rig`](#rig) | `gastown.tenev.io/v1alpha1` | Namespaced | `rig` | Per-rig desired topology |
| [`AgentRole`](#agentrole) | `gastown.tenev.io/v1alpha1` | Namespaced | `ar` | Custom agent roles |
| [`DoltInstance`](#doltinstance) | `gastown.tenev.io/v1alpha1` | Namespaced | `dolt` | Dolt StatefulSet + PVC + Service |

CRD manifests live in [`config/crd/bases/`](../config/crd/bases/).

---

## GasTown

`GasTown` is the top-level **cluster-scoped** resource. One CR per deployment
environment (dev / staging / prod). It names the backing `DoltInstance`, defines
town-wide agent defaults, and optionally manages a Surveyor Deployment.

### Spec fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `version` | string | **Yes** | — | Town topology schema version. Must match a version known to `town-ctl`. Pattern: `^\d+$`. |
| `home` | string | **Yes** | — | Base directory for Gas Town data on agent hosts (e.g. `~/.gt`). |
| `doltRef.name` | string | **Yes** | — | Name of the `DoltInstance` CR backing this town. |
| `doltRef.namespace` | string | **Yes** | — | Namespace of the `DoltInstance` CR. |
| `secretsRef.name` | string | No | — | Name of a Kubernetes `Secret` containing API keys and tokens. Values are injected as env vars into agent pods — never written to Dolt. |
| `defaults.mayorModel` | string | No | `claude-opus-4-6` | Default Claude model for Mayor agents across all rigs. |
| `defaults.polecatModel` | string | No | `claude-sonnet-4-6` | Default Claude model for Polecat agents across all rigs. |
| `defaults.maxPolecats` | integer | No | `20` | Default maximum concurrent Polecat agents per rig. Range: 1–100. |
| `agents.surveyor` | boolean | No | `false` | When `true`, the operator manages a Surveyor Deployment for this town. |
| `agents.surveyorClaudeMdRef.name` | string | Conditional | — | Name of a ConfigMap containing the Surveyor CLAUDE.md. Required when `agents.surveyor=true`. |
| `agents.surveyorImage` | string | No | — | Overrides the default Surveyor container image. |
| `agents.surveyorResources` | ResourceRequirements | No | — | Resource requests/limits for the Surveyor pod (standard Kubernetes format). |

### Status fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | Standard Kubernetes conditions: `Ready`, `SurveyorRunning`, `DesiredTopologyInSync`. |
| `observedGeneration` | integer | The `.metadata.generation` this status reflects. |
| `doltCommit` | string | Dolt commit hash of the last successful `desired_topology` write. |
| `lastReconcileAt` | datetime | When the Surveyor last completed a reconcile loop. |

### Mapping to `town.toml`

| CRD field | `town.toml` equivalent |
|-----------|------------------------|
| `spec.version` | `version` |
| `spec.home` | `town.home` |
| `spec.defaults.mayorModel` | `defaults.mayor_model` |
| `spec.defaults.polecatModel` | `defaults.polecat_model` |
| `spec.defaults.maxPolecats` | `defaults.max_polecats` |
| `spec.agents.surveyor` | `town.agents.surveyor` |
| `spec.secretsRef.name` | `secrets.*` (K8s Secret instead of env-var interpolation) |

### Minimal example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: GasTown
metadata:
  name: my-town
spec:
  version: "1"
  home: /home/gt/.gt
  doltRef:
    name: my-town-dolt
    namespace: gastown-system
```

### Full example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: GasTown
metadata:
  name: my-town
spec:
  version: "1"
  home: /home/gt/.gt
  doltRef:
    name: my-town-dolt
    namespace: gastown-system
  secretsRef:
    name: gastown-secrets
  defaults:
    mayorModel: claude-opus-4-6
    polecatModel: claude-sonnet-4-6
    maxPolecats: 20
  agents:
    surveyor: true
    surveyorClaudeMdRef:
      name: surveyor-claude-md
    surveyorResources:
      requests:
        cpu: "500m"
        memory: "512Mi"
      limits:
        cpu: "2"
        memory: "2Gi"
```

---

## Rig

`Rig` is a **namespaced** resource representing a single Gas Town rig: one git
repository and its agent pool.

### Spec fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `townRef` | string | **Yes** | — | Name of the `GasTown` CR this Rig belongs to. |
| `repo` | string | **Yes** | — | Filesystem path to the git repository for this rig. |
| `branch` | string | **Yes** | `main` | Git branch this rig tracks. |
| `enabled` | boolean | **Yes** | `true` | Controls whether this rig is active. Disabled rigs are drained and stopped by the Surveyor. |
| `agents.mayor` | boolean | No | `true` | Enables the Mayor agent for this rig. |
| `agents.witness` | boolean | No | `true` | Enables the Witness agent for this rig. |
| `agents.refinery` | boolean | No | `true` | Enables the Refinery agent for this rig. |
| `agents.deacon` | boolean | No | `true` | Enables the Deacon agent for this rig. |
| `agents.maxPolecats` | integer | No | (town default) | Overrides `GasTown.spec.defaults.maxPolecats` for this rig. Range: 1–100. |
| `agents.mayorModel` | string | No | (town default) | Overrides `GasTown.spec.defaults.mayorModel` for this rig. |
| `agents.polecatModel` | string | No | (town default) | Overrides `GasTown.spec.defaults.polecatModel` for this rig. |
| `agents.mayorClaudeMdRef.name` | string | No | — | Name of a ConfigMap containing the Mayor's CLAUDE.md for this rig. |
| `roles` | []string | No | `[]` | AgentRole names (in this namespace) opted into by this rig. Only `scope=rig` AgentRoles require opt-in. |
| `formulas` | []RigFormula | No | `[]` | Cron-driven Formula schedules active for this rig. |
| `formulas[].name` | string | **Yes** | — | Formula name. |
| `formulas[].schedule` | string | **Yes** | — | Standard 5-field cron expression (e.g. `0 2 * * *`). |

### Status fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `Ready`, `DesiredTopologyInSync`. |
| `observedGeneration` | integer | The `.metadata.generation` this status reflects. |
| `doltCommit` | string | Dolt commit hash of the last successful `desired_topology` write. |
| `lastConvergedAt` | datetime | When the rig last reached a fully converged state. |
| `rigHealth` | string | Health summary from `actual_topology`: `healthy`, `degraded`, or `unknown`. |
| `runningPolecats` | integer | Current number of running Polecat agents (from `actual_topology`). |

### Mapping to `town.toml`

| CRD field | `town.toml` equivalent |
|-----------|------------------------|
| `spec.repo` | `rig.repo` |
| `spec.branch` | `rig.branch` |
| `spec.enabled` | `rig.enabled` |
| `spec.agents.mayor` | `rig.agents.mayor` |
| `spec.agents.witness` | `rig.agents.witness` |
| `spec.agents.refinery` | `rig.agents.refinery` |
| `spec.agents.deacon` | `rig.agents.deacon` |
| `spec.agents.maxPolecats` | `rig.agents.max_polecats` |
| `spec.agents.polecatModel` | `rig.agents.polecat_model` |
| `spec.roles` | `rig.agents.roles` |
| `spec.formulas` | `[[rig.formula]]` |

### Minimal example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: Rig
metadata:
  name: backend
  namespace: gastown-system
spec:
  townRef: my-town
  repo: /home/gt/projects/backend
  branch: main
  enabled: true
```

### Full example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: Rig
metadata:
  name: backend
  namespace: gastown-system
spec:
  townRef: my-town
  repo: /home/gt/projects/backend
  branch: main
  enabled: true
  agents:
    mayor: true
    witness: true
    refinery: true
    deacon: true
    maxPolecats: 30
    polecatModel: claude-haiku-4-5-20251001
    mayorClaudeMdRef:
      name: backend-mayor-claude-md
  roles:
    - reviewer
  formulas:
    - name: nightly-tests
      schedule: "0 2 * * *"
```

---

## AgentRole

`AgentRole` is a **namespaced** resource that declares a custom agent archetype.
Activate per-rig (`scope=rig`) via `Rig.spec.roles`, or town-wide (`scope=town`)
without opt-in.

### Spec fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `townRef` | string | **Yes** | — | Name of the `GasTown` CR this role belongs to. |
| `scope` | enum | **Yes** | — | `rig` — activated per rig via opt-in; `town` — activated across all rigs. |
| `identity.claudeMdRef.name` | string | **Yes** | — | Name of a ConfigMap containing this role's CLAUDE.md. Inline content is not supported. |
| `trigger.type` | enum | **Yes** | — | Activation mechanism: `bead_assigned`, `schedule`, `event`, `manual`. |
| `trigger.schedule` | string | Conditional | — | Cron expression. Required when `trigger.type=schedule`. |
| `trigger.event` | string | Conditional | — | Event name polled via `bd search`. Required when `trigger.type=event`. |
| `supervision.parent` | string | **Yes** | — | Built-in or custom role that supervises this role. Must not equal this role's own name. |
| `supervision.reportsTo` | string | No | (parent) | Escalation target. Defaults to `supervision.parent` if empty. |
| `resources.maxInstances` | integer | No | `1` | Maximum concurrent instances of this role. Minimum: 1. |

### Status fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `Ready`, `DesiredTopologyInSync`. |
| `observedGeneration` | integer | The `.metadata.generation` this status reflects. |
| `doltCommit` | string | Dolt commit hash of the last successful `desired_custom_roles` write. |
| `activeInstances` | integer | Current number of running instances of this role (from `actual_topology`). |
| `lastSeenAt` | datetime | When an instance of this role was last observed active. |

### Mapping to `town.toml`

| CRD field | `town.toml` equivalent |
|-----------|------------------------|
| `spec.scope` | `role.scope` |
| `spec.identity.claudeMdRef.name` | `role.identity.claude_md` (ConfigMap instead of file path) |
| `spec.trigger.type` | `role.trigger.type` |
| `spec.trigger.schedule` | `role.trigger.schedule` |
| `spec.trigger.event` | `role.trigger.event` |
| `spec.supervision.parent` | `role.supervision.parent` |
| `spec.supervision.reportsTo` | `role.supervision.reports_to` |
| `spec.resources.maxInstances` | `role.resources.max_instances` |

### Minimal example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: AgentRole
metadata:
  name: reviewer
  namespace: gastown-system
spec:
  townRef: my-town
  scope: rig
  identity:
    claudeMdRef:
      name: reviewer-claude-md
  trigger:
    type: bead_assigned
  supervision:
    parent: witness
```

### Full example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: AgentRole
metadata:
  name: reviewer
  namespace: gastown-system
spec:
  townRef: my-town
  scope: rig
  identity:
    claudeMdRef:
      name: reviewer-claude-md
  trigger:
    type: schedule
    schedule: "0 * * * *"
  supervision:
    parent: witness
    reportsTo: mayor
  resources:
    maxInstances: 3
```

---

## DoltInstance

`DoltInstance` is a **namespaced** resource that manages a single Dolt SQL server
backing a Gas Town instance. The operator reconciles a StatefulSet, a headless
Service, and a PVC.

### Spec fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `version` | string | **Yes** | — | Dolt container image tag (e.g. `v1.43.4`). |
| `replicas` | integer | **Yes** | `1` | Must be `1` in MVP. Field present for API stability. |
| `storage.size` | resource.Quantity | **Yes** | — | PVC capacity (e.g. `10Gi`). |
| `storage.storageClassName` | string | No | (cluster default) | StorageClass to use for the PVC. |
| `service.port` | integer | No | `3306` | Port on which the Service exposes Dolt's MySQL-compatible endpoint. |
| `service.type` | enum | No | `ClusterIP` | Kubernetes Service type: `ClusterIP`, `NodePort`, or `LoadBalancer`. |

### Status fields

| Field | Type | Description |
|-------|------|-------------|
| `conditions` | []Condition | `Ready`, `DDLInitialized`, `VersionMigrationRequired`. |
| `observedGeneration` | integer | The `.metadata.generation` this status reflects. |
| `currentVersion` | string | Dolt image tag currently running. |
| `endpoint` | string | In-cluster DNS address, e.g. `dolt-instance.gastown-system.svc.cluster.local:3306`. |

### Mapping to `town.toml`

`DoltInstance` has no direct `town.toml` equivalent — it provisions the Dolt
infrastructure that `town-ctl` assumes is already running. For local setups,
run `dolt sql-server` directly; `DoltInstance` is the K8s equivalent.

| CRD field | Local equivalent |
|-----------|-----------------|
| `spec.version` | Dolt binary version (`dolt version`) |
| `spec.service.port` | `town.dolt_port` |

### Minimal example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: DoltInstance
metadata:
  name: my-town-dolt
  namespace: gastown-system
spec:
  version: v1.43.4
  replicas: 1
  storage:
    size: 10Gi
```

### Full example

```yaml
apiVersion: gastown.tenev.io/v1alpha1
kind: DoltInstance
metadata:
  name: my-town-dolt
  namespace: gastown-system
spec:
  version: v1.43.4
  replicas: 1
  storage:
    size: 50Gi
    storageClassName: fast-ssd
  service:
    port: 3306
    type: ClusterIP
```

---

## Getting started on Kubernetes

### 1. Apply the CRD manifests

```bash
kubectl apply -f config/crd/bases/
```

This registers all four CRDs with your cluster.

### 2. Create the `gastown-system` namespace

```bash
kubectl create namespace gastown-system
```

### 3. Create the `DoltInstance` CR

```bash
kubectl apply -f - <<'EOF'
apiVersion: gastown.tenev.io/v1alpha1
kind: DoltInstance
metadata:
  name: my-town-dolt
  namespace: gastown-system
spec:
  version: v1.43.4
  replicas: 1
  storage:
    size: 10Gi
EOF
```

Wait for it to become ready:

```bash
kubectl wait doltinstance/my-town-dolt -n gastown-system \
  --for=condition=Ready --timeout=120s
```

### 4. Create the `GasTown` CR

```bash
kubectl apply -f - <<'EOF'
apiVersion: gastown.tenev.io/v1alpha1
kind: GasTown
metadata:
  name: my-town
spec:
  version: "1"
  home: /home/gt/.gt
  doltRef:
    name: my-town-dolt
    namespace: gastown-system
  defaults:
    mayorModel: claude-opus-4-6
    polecatModel: claude-sonnet-4-6
    maxPolecats: 20
EOF
```

### 5. Create a `Rig` CR

```bash
kubectl apply -f - <<'EOF'
apiVersion: gastown.tenev.io/v1alpha1
kind: Rig
metadata:
  name: backend
  namespace: gastown-system
spec:
  townRef: my-town
  repo: /home/gt/projects/backend
  branch: main
  enabled: true
EOF
```

### 6. Verify

```bash
kubectl get gastowns,rigs -A
```

Expected output:

```
NAMESPACE          NAME                           AGE
                   gastown.gastown.tenev.io/my-town   30s

NAMESPACE          NAME                       AGE
gastown-system     rig.gastown.tenev.io/backend   20s
```

Check conditions:

```bash
kubectl describe gastown my-town
kubectl describe rig backend -n gastown-system
```
