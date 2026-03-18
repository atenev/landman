# ADR-0005: Kubernetes Operator and CRD Design for Gas Town

- **Status**: Proposed
- **Date**: 2026-03-17
- **Beads issue**: dgt-3j8
- **Deciders**: Aleksandar Tenev
- **Depends on**: ADR-0001, ADR-0002, ADR-0003, ADR-0004

---

## Context

ADR-0001 established `town-ctl` as the reference actuator: it reads `town.toml` and writes
`desired_topology` to Dolt. ADR-0001 Decision 2 explicitly anticipated multiple actuators
consuming the same contract:

> "The same `town.toml` can be consumed by: a local `town-ctl` binary (CLI, dev laptop),
> a K8s operator (production cluster, dgt-3j8), a Flux/ArgoCD plugin."

Gas Town targets 600 concurrent agents on multi-host Kubernetes. The `town-ctl` + systemd
model works correctly on a single host or VM but does not provide:

- **Pod scheduling**: which cluster node runs which rig
- **Restart policies**: pod-level crash recovery managed by the cluster
- **Resource limits**: CPU and memory bounds per rig and per agent pool
- **K8s-native GitOps**: ArgoCD/Flux applies CRDs from a git repo and the operator reconciles
- **`kubectl` observability**: `kubectl get rig -o wide` shows operational health

A Kubernetes operator closes this gap. It is a second actuator in the multi-actuator model
established by ADR-0001 — not a replacement for `town-ctl`, not a parallel reconciler, and
not a modification to the `gt` binary. Its only write surface is `desired_topology` in Dolt.

**Scope constraint**: Gas Town must remain fully operational without Kubernetes. The K8s
operator is an optional deployment adapter for cluster environments. A developer running a
single-rig project on a local VM uses only `town-ctl` and never installs the operator.

---

## Decisions

### Decision 1: The K8s operator is an optional actuator — not a required component

**Chosen**: The operator is a standalone binary (`gastown-operator`) deployed only in
Kubernetes environments. It is optional at every scale: local laptop, single VM, or
multi-host cluster. No Gas Town component depends on or detects the operator.

**Rationale**:

The declarative topology stack (ADR-0001 through ADR-0004) is complete and self-contained
without Kubernetes. `town-ctl` + `town.toml` covers all non-cluster use cases. The operator
is a deployment adapter — it translates Kubernetes primitives (CRDs) into the same
`desired_topology` Dolt writes that `town-ctl` performs.

```
Deployment context        Actuator              Dolt desired_topology
──────────────────────────────────────────────────────────────────────
Laptop / single VM   →   town-ctl apply    →   same tables
Baremetal server     →   town-ctl apply    →   same tables
K8s cluster          →   K8s operator      →   same tables
GitOps (ArgoCD)      →   K8s operator      →   same tables (via CRD apply)
```

The Surveyor (ADR-0002) is indifferent to which actuator wrote `desired_topology`. Any
writer — `town-ctl`, the operator, or direct SQL — triggers the same reconcile loop.

---

### Decision 2: Four CRDs — GasTown, Rig, AgentRole, DoltInstance

**Chosen**: Four CRDs that map directly to the `town.toml` sections defined in ADR-0001 and
ADR-0004:

| CRD | Scope | Maps to | Manages |
|-----|-------|---------|---------|
| `GasTown` | Cluster-scoped | `[town]` + `[defaults]` | Town metadata, Surveyor Deployment |
| `Rig` | Namespaced | `[[rig]]` | Rig desired topology rows in Dolt |
| `AgentRole` | Namespaced | `[[role]]` | Custom role rows in Dolt (ADR-0004) |
| `DoltInstance` | Namespaced | (infrastructure) | Dolt StatefulSet + Service + PVC |

**No `PolecatPool` CRD**: `max_polecats` is a static field in `Rig.spec.agents`. Dynamic
polecat scaling is deferred to the ScalingAgent custom role (dgt-5g5), which writes through
the Dolt contract rather than K8s autoscaling primitives. An HPA-based approach would
bypass the Surveyor and requires a custom metrics adapter — both add complexity without
validated operational need.

**No cross-namespace Rigs**: all Rig and AgentRole objects live in the same namespace as
their referenced GasTown CR's backing DoltInstance. Multi-namespace topology is out of
scope for this ADR.

**Rationale for cluster-scoped GasTown**:
A Gas Town town is a singleton coordination entity — one Dolt instance, one Surveyor. Cluster
scope prevents accidental duplication and aligns with the mental model: one town per cluster
(or per environment with distinct clusters). GasTown objects do not carry sensitive data; all
secrets are in `spec.secretsRef`.

---

### Decision 3: Operator writes desired_topology_versions first — ADR-0003 compliance is a contract

**Chosen**: Every Dolt transaction written by any operator controller MUST upsert
`desired_topology_versions` as the first SQL statement, before any topology data rows.
This is identical to the requirement placed on `town-ctl` by ADR-0003.

**Rationale**:

ADR-0003 Decision 2 states: "Any future actuator (K8s operator, GitOps controller) writing
`desired_topology` tables must also write `desired_topology_versions` first. This is a
contract, not a convention."

The operator is that future actuator. The contract applies identically. The Surveyor's
pre-flight check (ADR-0003 Decision 3) makes no distinction between rows written by
`town-ctl` and rows written by the operator — it only checks schema version. An operator
that skips the `desired_topology_versions` upsert will cause the Surveyor to escalate to
Mayor on the next reconcile loop with "version record missing."

---

### Decision 4: Dolt as a StatefulSet managed by the DoltInstance controller — single-primary for MVP

**Chosen**: The `DoltInstanceController` manages a Kubernetes StatefulSet running the Dolt
container, a headless Service, and a PersistentVolumeClaim. For MVP, `spec.replicas` is
hardcoded to `1` and the controller rejects values greater than `1` with a clear error.

**Alternatives considered**:

**Option A — User-managed Dolt, operator receives a connection string**

```yaml
spec:
  doltEndpoint: my-dolt.gastown-system.svc.cluster.local:3306
```

Simpler operator, but pushes Dolt lifecycle management to the user. No K8s-native story for
Dolt schema migrations, storage sizing, or version upgrades. Rejected for MVP — the operator
should provide a complete, self-contained deployment.

**Option B — DoltInstance CRD (chosen)**

Follows the pattern of mysql-operator and postgres-operator: a CRD describes the desired
Dolt instance; the controller ensures the StatefulSet, Service, and PVC match. The operator
initialises `desired_topology` DDL on first startup and runs schema migrations on version
upgrades.

`DoltInstance.spec.replicas: 1` is the only supported value in MVP. The field exists in the
schema so the API is stable; the controller prints a clear error if `replicas > 1`:

```
DoltInstance: replicas > 1 not supported in operator v0.x
Multi-primary Dolt replication is deferred. See dgt-XXX.
```

Replication is deferred until there is empirical evidence of a Dolt read bottleneck at scale
(expected at 300+ concurrent agents all polling `actual_topology`).

---

### Decision 5: Surveyor managed as a Deployment by the GasTownController

**Chosen**: When `GasTown.spec.agents.surveyor = true`, the `GasTownController` ensures a
Surveyor Deployment exists in the same namespace as the GasTown CR. The operator creates and
updates this Deployment; it does not know what the Surveyor does internally.

**Rationale**:

ADR-0002 Decision 6 established that `town-ctl apply` ensures the Surveyor is running when
`[town.agents] surveyor = true`. In a K8s environment, `town-ctl` is replaced by the
operator as the actuator. The operator takes on the same Surveyor lifecycle responsibility.

The operator's Surveyor knowledge is minimal: it knows the container image, the Dolt
connection env vars to inject, and the CLAUDE.md ConfigMap to mount. It does not know the
Surveyor's internal reconcile protocol. This is the same separation ADR-0002 maintains for
`town-ctl`.

The Surveyor Deployment uses a dedicated ServiceAccount with only the permissions it needs:
Dolt connection (via env vars), Beads access (filesystem or Dolt), and no Kubernetes API
access. The Surveyor is not a K8s controller and has no `kubectl` permissions.

---

### Decision 6: CLAUDE.md files stored in ConfigMaps, mounted as volumes

**Chosen**: Each agent role that requires a CLAUDE.md (Surveyor, Mayor per rig, custom
roles) references a Kubernetes ConfigMap by name. The operator mounts the ConfigMap as a
read-only volume into the relevant pod. No inline CLAUDE.md content in CRD specs.

**Rationale**:

ADR-0004 Decision 2 established "CLAUDE.md as a file reference only — no inline content" for
custom roles. The same principle applies to all K8s-managed agent identities. A ConfigMap is
the Kubernetes equivalent of a file path: it is independently versioned, diffable in git
alongside the CRD manifests, and mountable into any pod that references it.

Inline content in a CRD spec would embed agent identity in the infrastructure manifest —
exactly what ADR-0004 rejected for `town.toml`.

---

### Decision 7: Secrets via K8s Secrets, never written to Dolt

**Chosen**: `GasTown.spec.secretsRef` names a Kubernetes Secret containing API keys and
tokens. The operator injects Secret values as environment variables into agent pods (Surveyor,
rig pods). Secrets are never written to Dolt tables, CRD specs, or operator logs.

**Rationale**:

ADR-0001 Decision 4 is unconditional: secrets are resolved at apply time and injected as
env vars into agent processes. They are never written to Dolt, to `town.toml`, or to log
files. The operator upholds this invariant: it reads the K8s Secret and passes values as
env vars to pod specs. The pod spec does not embed the secret values — it references the
Secret via `envFrom`, so Kubernetes injects them at runtime.

External Secrets Operator (ESO) and Vault Agent injection work transparently: both surface
their secrets as Kubernetes Secrets, which the operator references via `secretsRef`. No
special operator support required for these integrations.

---

### Decision 8: Status fields surface actual_topology data from Dolt

**Chosen**: Each controller runs a secondary status-sync loop: it reads `actual_topology`
tables from Dolt and writes the results to the corresponding CRD's `.status` subresource.
This gives operators `kubectl get rig -o wide` visibility into operational health without a
separate monitoring tool.

**Fields surfaced per CRD**:

| CRD | Status fields from actual_topology |
|-----|------------------------------------|
| `GasTown` | `surveyorRunning`, `lastReconcileAt`, `reconcileHealth` |
| `Rig` | `runningPolecats`, `rigHealth`, `lastConvergedAt`, `doltCommit` |
| `AgentRole` | `activeInstances`, `lastSeenAt` |
| `DoltInstance` | `ready`, `currentVersion`, `endpoint` |

The status loop runs independently of the spec-reconcile loop, on a configurable interval
(default: 30s). It is read-only with respect to Dolt — it never writes `desired_topology`.

---

### Decision 9: Admission webhooks enforce ADR invariants at apply time

**Chosen**: Validating and mutating admission webhooks are deployed alongside the operator
to enforce constraints that come from ADR-0001 through ADR-0004.

**Validating webhooks**:

| Resource | Validation enforced |
|----------|---------------------|
| `AgentRole` | `metadata.name` not in reserved built-in list (ADR-0004 D4) |
| `AgentRole` | `spec.supervision.parent` non-empty (ADR-0004 D3) |
| `AgentRole` | `spec.supervision.parent` not equal to `metadata.name` (no self-supervision) |
| `Rig` | `spec.townRef` resolves to an existing GasTown in the cluster |
| `GasTown` | `spec.doltRef` resolves to an existing DoltInstance |
| `DoltInstance` | `spec.replicas` == 1 (MVP constraint, Decision 4) |

**Mutating webhooks**:

| Resource | Default applied |
|----------|----------------|
| `Rig` | `spec.agents.mayorModel` ← `GasTown.spec.defaults.mayorModel` if unset |
| `Rig` | `spec.agents.polecatModel` ← `GasTown.spec.defaults.polecatModel` if unset |
| `Rig` | `spec.agents.maxPolecats` ← `GasTown.spec.defaults.maxPolecats` if unset |

These webhooks make the operator's safety properties structural rather than conventional.
An `AgentRole` that shadows a built-in role name cannot be created — it is rejected at the
Kubernetes API server before the controller ever sees it.

---

### Decision 10: RBAC — operator never manages Gas Town agent pods directly

**Chosen**: The operator's ServiceAccount has permissions for: CRD objects, ConfigMaps
(read), Secrets (read), Deployments and StatefulSets (for Dolt + Surveyor only), Services,
PVCs, and Events. It does not have permissions to manage arbitrary Pods.

**Rationale**:

Gas Town agent pods (rig pods running `gt`, polecat pools) are created by Dogs executing
Surveyor Beads — not by the operator. The operator manages exactly two K8s workloads:
the Dolt StatefulSet (via DoltInstance) and the Surveyor Deployment (via GasTown). All
other agent lifecycle is delegated to the Surveyor + Dogs via the Beads coordination
primitive, as specified in ADR-0002.

This RBAC boundary is a correctness constraint: an operator with broad Pod permissions
could accidentally bypass the Surveyor's convergence verification and create an inconsistent
actual topology state.

---

## Consequences

### What becomes easier

- **K8s-native GitOps**: `GasTown` and `Rig` CRDs committed to git, ArgoCD applies them,
  operator reconciles — full declarative topology with K8s tooling and no Kubernetes-specific
  Gas Town knowledge required in CI.
- **Environment promotion**: promote a topology from dev to prod by applying the same CRD
  manifests to a different cluster. Secrets and resource limits differ via K8s mechanisms
  (namespace-scoped Secrets, resource quotas).
- **Operational visibility**: `kubectl get rig -o wide` shows running polecats, rig health,
  and last convergence time. No additional monitoring tool required for basic health checks.
- **Crash recovery**: StatefulSet ensures Dolt pod restarts on crash. Surveyor Deployment
  ensures the reconciler restarts. Gas Town's GUPP invariant handles the re-reconcile.
- **`town-ctl` parity**: the operator writes the same `desired_topology` schema as `town-ctl`.
  A team can switch from `town-ctl apply` to K8s operator without any Dolt schema migration.

### New constraints introduced

- **`gastown-operator` is a new deployed component** in K8s environments. It must be
  versioned, upgraded, and monitored like any other operator. Its RBAC permissions must be
  audited when upgrading.
- **DoltInstance is the Dolt lifecycle owner** in K8s. Operators who want to bring their own
  Dolt must use `spec.doltEndpoint` (Decision 4 Option A) — a future extension, not MVP.
- **CRDs are the desired-state write path in K8s**. Direct `town-ctl apply` against a
  Kubernetes-managed Dolt instance is not prohibited but will create a split-brain between
  CRD spec and `desired_topology` state. Teams should choose one actuator per environment.
- **Admission webhooks are required** for correctness guarantees. Deploying the operator
  CRDs without the webhooks loses the ADR invariant enforcement at the API layer.

### Out of scope for this ADR

- Dolt replication / multi-primary (→ defer to when scale evidence exists)
- PolecatPool CRD / HPA-based autoscaling (→ dgt-5g5 ScalingAgent custom role)
- Cross-namespace Rig references
- `town-ctl export --backend=k8s` (→ dgt-6u7)
- `kubectl` plugin for Gas Town operations
- Operator upgrade / migration tooling between operator versions
- Multi-cluster Gas Town federation
- Operator OLM (Operator Lifecycle Manager) packaging

---

## Reference: CRD Skeleton

```yaml
# GasTown — cluster-scoped
apiVersion: gastown.io/v1alpha1
kind: GasTown
metadata:
  name: my-town
spec:
  version: "1"
  home: /gt
  doltRef:
    name: my-town-dolt
    namespace: gastown-system
  defaults:
    mayorModel: claude-opus-4-6
    polecatModel: claude-sonnet-4-6
    maxPolecats: 20
  agents:
    surveyor: true
    surveyorClaudeMdRef:
      name: surveyor-claude-md   # ConfigMap
  secretsRef:
    name: gastown-secrets        # K8s Secret — never written to Dolt

---
# Rig — namespaced
apiVersion: gastown.io/v1alpha1
kind: Rig
metadata:
  name: backend
  namespace: gastown-system
spec:
  townRef: my-town
  repo: /workspaces/backend
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
      name: backend-mayor-claude-md  # ConfigMap
  formulas:
    - name: nightly-tests
      schedule: "0 2 * * *"
  roles:
    - reviewer

---
# AgentRole — namespaced
apiVersion: gastown.io/v1alpha1
kind: AgentRole
metadata:
  name: reviewer
  namespace: gastown-system
spec:
  townRef: my-town
  scope: rig
  identity:
    claudeMdRef:
      name: reviewer-claude-md  # ConfigMap
  trigger:
    type: bead_assigned
  supervision:
    parent: witness
    reportsTo: mayor
  resources:
    maxInstances: 3

---
# DoltInstance — namespaced
apiVersion: gastown.io/v1alpha1
kind: DoltInstance
metadata:
  name: my-town-dolt
  namespace: gastown-system
spec:
  version: "1.42.0"
  replicas: 1           # only supported value in MVP
  storage:
    storageClassName: standard
    size: 50Gi
  service:
    type: ClusterIP
    port: 3306
```
