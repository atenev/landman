## Why

Gas Town targets 600 concurrent agents on multi-host Kubernetes. The existing `town-ctl` +
systemd model correctly handles single-host and VM deployments but provides no path to K8s
scheduling, pod-level crash recovery, resource limits, or GitOps-via-kubectl workflows.

This change introduces a Kubernetes operator that serves as an optional second actuator in
the multi-actuator model established by ADR-0001. It translates Kubernetes CRDs into the
same `desired_topology` Dolt writes that `town-ctl` performs. The Surveyor, Dogs, and the
rest of the Gas Town agent hierarchy are unchanged.

**Gas Town without Kubernetes remains fully supported.** This change adds a K8s deployment
path — it does not require K8s for any use case.

## What Changes

- Introduce four CRDs: `GasTown` (cluster-scoped), `Rig`, `AgentRole`, `DoltInstance`
  (namespaced). These CRDs mirror the `town.toml` sections defined in ADR-0001 and ADR-0004.
- Introduce `gastown-operator`: a Go binary containing four controllers (one per CRD), a
  status-sync loop, and admission webhooks. Deployed as a Deployment in K8s.
- Introduce the `DoltInstance` controller: manages a Dolt StatefulSet, headless Service, and
  PVC. Initialises `desired_topology` DDL and runs schema migrations on version upgrades.
- The `GasTownController` and `RigController` write `desired_topology_versions` first in
  every Dolt transaction — ADR-0003 compliance is structurally enforced in the operator.
- The `GasTownController` ensures the Surveyor Deployment exists when
  `spec.agents.surveyor = true`. It injects Dolt connection env vars and mounts the Surveyor
  CLAUDE.md ConfigMap.
- CLAUDE.md files are stored in ConfigMaps and mounted as volumes — never inlined in CRD
  specs.
- Secrets are referenced via `spec.secretsRef` (a K8s Secret name) and injected as env vars
  into pods. Never written to Dolt.
- Admission webhooks enforce ADR invariants at the Kubernetes API layer: reserved role name
  protection (ADR-0004 D4), mandatory supervision (ADR-0004 D3), `doltRef` validation, and
  default inheritance from GasTown to Rig.
- CRD status fields surface `actual_topology` data from Dolt: polecat counts, rig health,
  last convergence time — visible via `kubectl get rig -o wide`.

## Capabilities

### New Capabilities

- `crd-schema`: Four CRD schemas (`GasTown`, `Rig`, `AgentRole`, `DoltInstance`) with
  OpenAPI v3 validation. Go type definitions (`types.go`), DeepCopy generated code, and
  JSON Schema for each CRD. Cluster-scoped GasTown; namespaced Rig, AgentRole, DoltInstance.
- `operator-controllers`: Four controllers with reconcile loops, status-sync loops, and
  error handling. Each controller writes `desired_topology_versions` first per ADR-0003.
  GasTownController also manages the Surveyor Deployment lifecycle.
- `dolt-in-k8s`: DoltInstance controller manages a Dolt StatefulSet (single-primary, MVP).
  Handles storage provisioning, schema migration on version upgrade, and readiness gating for
  dependent controllers.
- `admission-webhooks`: Validating webhook for ADR invariants. Mutating webhook for default
  inheritance (Rig inherits GasTown defaults). TLS managed by cert-manager.

### Modified Capabilities

- `town-toml-manifest` (from `declarative-town-topology`): no schema changes. The operator
  writes the same `desired_topology` tables. `town-ctl` and the operator are interchangeable
  actuators for the same contract.

## Impact

- New binary: `gastown-operator` (Go, separate from `gt` and `town-ctl`)
- New K8s objects installed by operator: CRDs, RBAC (ClusterRole, ClusterRoleBinding,
  Role, RoleBinding), Deployment, Service (webhook), cert-manager Certificate
- No new Dolt tables beyond those defined in `declarative-town-topology`,
  `surveyor-topology-reconciler`, and `custom-roles-schema` changes
- No modification to `gt` binary
- No modification to `town-ctl` binary
- `town-ctl apply` remains fully functional — operator and `town-ctl` are alternative
  actuators; teams choose one per environment
- Depends on: `declarative-town-topology`, `surveyor-topology-reconciler`,
  `custom-roles-schema`
