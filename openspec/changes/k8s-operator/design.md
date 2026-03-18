## Context

ADR-0001 established Dolt as the actuator coupling point and anticipated multiple actuators
writing to the same `desired_topology` schema. ADR-0005 designs the Kubernetes operator as
that second actuator.

The operator sits in the pipeline between Kubernetes CRDs and Dolt:

```
CRD apply (kubectl / ArgoCD / Flux)
        │
        ▼
gastown-operator (watches CRDs, writes Dolt)
        │
        ▼
Dolt desired_topology  ←── same tables as town-ctl writes
        │
        ▼
Surveyor (ADR-0002) → Dog Beads → Gas Town agents
```

The operator has no knowledge of Gas Town internals beyond the Dolt schema. It does not
manage rig pods, polecat pools, or agent processes — that is the Surveyor's domain.

**Constraint carried from all prior ADRs**: no modification to the `gt` binary.

## Goals / Non-Goals

**Goals:**
- Define Go CRD types for GasTown, Rig, AgentRole, DoltInstance
- Specify reconcile loop logic for each of the four controllers
- Specify DoltInstance StatefulSet management (schema init, migration, readiness gating)
- Specify Surveyor Deployment management by GasTownController
- Specify admission webhook rules enforcing ADR-0001 through ADR-0004 invariants
- Specify status-sync loop: actual_topology → CRD status fields
- RBAC: operator ServiceAccount permissions (minimum viable)

**Non-Goals:**
- Dolt replication / multi-primary Dolt (deferred)
- PolecatPool CRD / HPA autoscaling (→ dgt-5g5 ScalingAgent)
- Cross-namespace Rig references
- `town-ctl export --backend=k8s` (→ dgt-6u7)
- `kubectl` plugin for Gas Town operations
- Operator Lifecycle Manager (OLM) packaging
- Modifying `gt` binary in any way

## Decisions

### D1: Operator is an actuator — it writes desired_topology, never manages agent pods

The operator's only Dolt write surface is `desired_topology_*` tables. It does not create,
delete, or update Gas Town agent pods (rig pods, polecat pools, Mayor/Deacon processes).
Agent lifecycle is the Surveyor's responsibility via Dog Beads.

The operator manages exactly two Kubernetes workloads:
1. Dolt StatefulSet (via DoltInstance controller)
2. Surveyor Deployment (via GasTownController, when `spec.agents.surveyor = true`)

This RBAC boundary is enforced structurally: the operator ServiceAccount has no permissions
to manage arbitrary Pods beyond these two workloads.

### D2: GasTown is cluster-scoped; Rig, AgentRole, DoltInstance are namespaced

A Gas Town town is a singleton coordination entity. Cluster scope prevents accidental
duplication and gives a clear mental model: one GasTown CR per deployment environment (dev,
staging, prod each live in separate clusters or are expressed as separate GasTown CRs).

All Rig and AgentRole objects live in the same namespace as the DoltInstance backing their
GasTown CR. No cross-namespace references.

### D3: DoltInstance controller gates all other controllers on Dolt readiness

The RigController, AgentRoleController, and GasTownController do not attempt Dolt writes
until `DoltInstance.status.conditions[Ready] = true`. This prevents write failures during
initial cluster setup or Dolt pod restarts.

The readiness gate is implemented as a pre-reconcile check in each controller:
```go
dolt, err := r.getDoltInstance(ctx, gastownSpec.DoltRef)
if !dolt.IsReady() {
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
```

### D4: ADR-0003 compliance enforced in controller code, not documentation

Every controller function that opens a Dolt transaction calls `upsertTopologyVersions()`
as the first SQL statement. This is a shared utility in the operator codebase, not an
ad-hoc per-controller implementation. A code review checklist item: any Dolt write path
that does not call `upsertTopologyVersions()` first is a bug.

```go
func (r *RigReconciler) syncToDolt(ctx context.Context, rig *v1alpha1.Rig) error {
    tx, err := r.dolt.BeginTx(ctx)
    // ADR-0003: ALWAYS first
    if err := tx.UpsertTopologyVersions(ctx, tableVersions); err != nil {
        return err
    }
    // ... topology data writes follow
}
```

### D5: Surveyor Deployment spec is minimal — operator does not own Surveyor's internal config

The GasTownController creates a Deployment for the Surveyor with:
- Container image: from `GasTown.spec.agents.surveyorImage` (or operator default)
- CLAUDE.md: mounted from `spec.agents.surveyorClaudeMdRef` ConfigMap
- Dolt connection: env vars from `DoltInstance.status.endpoint` + `spec.secretsRef`
- Restart policy: `Always` (Deployment default)
- No resource limits set by operator — user adds these to the Deployment via `spec.agents.surveyorResources`

The operator does not configure the Surveyor's reconcile parameters, TTLs, or convergence
thresholds. Those live in the Surveyor's CLAUDE.md (ADR-0002 D6). The operator only ensures
the process is running with the right identity and connectivity.

### D6: Status sync reads actual_topology on a 30s poll, never writes desired_topology

A separate goroutine per controller polls Dolt `actual_topology` tables and patches CRD
`.status` subresources. This is read-only with respect to Dolt. The poll interval is
configurable via operator flag `--status-sync-interval` (default: 30s).

Status conditions follow the standard Kubernetes convention:
- `Ready`: overall health
- `DesiredTopologyInSync`: Dolt write succeeded for latest generation
- `Converged`: `actual_topology` matches `desired_topology` (populated by status sync)

### D7: Admission webhooks require cert-manager for TLS

Kubernetes admission webhooks require HTTPS. The operator webhook Service uses a TLS
certificate managed by cert-manager (the de-facto standard in K8s operator tooling). The
operator Helm chart lists cert-manager as a required dependency. Environments without
cert-manager must either install it or manage TLS manually via the `--webhook-tls-cert-dir`
operator flag.

## Risks / Trade-offs

- **Split-brain between town-ctl and operator**: if both `town-ctl apply` and the K8s
  operator write to the same Dolt instance, `desired_topology` may reflect a mix of both
  sources. Mitigated by: the `desired_topology_versions` table records `written_by`
  (ADR-0003), making the last writer visible. Teams should choose one actuator per
  environment. The operator does not detect or block `town-ctl` writes.
- **Dolt StatefulSet single point of failure**: `replicas: 1` means Dolt pod crash = Dolt
  unavailable until pod restarts. Mitigated by StatefulSet restart policy and PVC persistence.
  `actual_topology` may lag during the restart window. Deferred to replication work.
- **Webhook TLS bootstrapping**: cert-manager adds a dependency. Mitigated by the
  `--webhook-tls-cert-dir` escape hatch for environments that manage TLS separately.
- **Operator version vs Dolt schema version skew**: if the operator is upgraded but the
  `desired_topology_versions` schema version it writes is unknown to the current Surveyor,
  the Surveyor will escalate to Mayor. This is the correct behaviour — but operators should
  upgrade the Surveyor and the operator together, or upgrade the Surveyor first.

## Migration Plan

1. Install cert-manager (if not present)
2. `helm install gastown-operator` — installs CRDs, RBAC, operator Deployment, webhook Service
3. Create a `DoltInstance` CR — operator creates Dolt StatefulSet, waits for ready
4. Author and apply `GasTown` CR pointing to DoltInstance — operator writes town desired_topology,
   creates Surveyor Deployment if `spec.agents.surveyor = true`
5. Apply `Rig` CRs — operator writes rig desired_topology rows; Surveyor reconciles
6. Apply `AgentRole` CRs — operator writes custom role rows
7. `town-ctl apply` is no longer needed for this cluster — operator is the actuator

For migration from an existing `town-ctl`-managed Dolt:
- Skip step 3: point `GasTown.spec.doltRef` at the existing Dolt (or wrap it in a DoltInstance
  CR after the fact)
- Existing `desired_topology` rows are valid — operator's first write upserts
  `desired_topology_versions` and any changed rows; unchanged rows remain

## Open Questions

- **Operator image registry and versioning**: where is `gastown-operator` published?
  (→ separate packaging/release decision)
- **Helm chart structure**: single chart with sub-charts for cert-manager dependency, or flat?
  (→ defer to packaging work)
- **Dolt endpoint for existing Dolt instances**: Option A from Decision 4 (user-managed Dolt
  with `spec.doltEndpoint`) is useful for migration and advanced users. Should it be in MVP
  or V2? (→ open, recommend V2)
