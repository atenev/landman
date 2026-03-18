# Spec: Operator Controllers

## Overview

Four controllers, each owning one CRD. All share two utilities:
- `upsertTopologyVersions(tx, versions)` — ADR-0003 compliance
- `getDoltConnection(ctx, gastownName)` — resolves Dolt endpoint via GasTown → DoltInstance chain

Controllers are implemented using controller-runtime. Each controller:
1. Watches its primary CRD
2. Watches its secondary dependencies (ConfigMaps, DoltInstance status)
3. Has a spec-reconcile function (writes desired_topology)
4. Has a status-sync function (reads actual_topology, patches CRD status)

## DoltInstanceController

**Watches**: DoltInstance

**Reconcile loop**:
```
1. Fetch DoltInstance spec
2. Reconcile StatefulSet
   a. Desired StatefulSet = dolt:<spec.version> image, 1 replica, PVC mount at /var/lib/dolt
   b. Create if missing; update if image version changed
   c. If version changed: set condition VersionMigrationRequired, run migration job
3. Reconcile headless Service (port spec.service.port → 3306)
4. Reconcile PVC (spec.storage.storageClassName, spec.storage.size)
5. Wait for StatefulSet ready (all replicas available)
6. On ready:
   a. Run DDL init job if first startup (desired_topology tables not present)
   b. Update status.endpoint = "<name>.<namespace>.svc.cluster.local:<port>"
   c. Set condition Ready=true
7. Requeue on NotReady (every 10s until ready)
```

**DDL init job**: a one-shot Kubernetes Job that runs `dolt sql < desired_topology_init.sql`.
The SQL file is embedded in the operator binary. On schema version upgrade, a migration Job
runs the appropriate `desired_topology_migrate_vN_to_vM.sql`.

**Status conditions**:
- `Ready`: StatefulSet all replicas available
- `DDLInitialized`: desired_topology tables present
- `VersionMigrationRequired`: current image version < spec.version

## GasTownController

**Watches**: GasTown, DoltInstance (for readiness)

**Reconcile loop**:
```
1. Fetch GasTown spec
2. Resolve DoltInstance via spec.doltRef → check DoltInstance.status.conditions[Ready]
   If not ready: requeue in 10s, set condition DesiredTopologyInSync=false
3. Open Dolt connection
4. BEGIN TRANSACTION
   a. upsertTopologyVersions(tx, {desired_town: currentSchemaVersion})  ← ADR-0003
   b. UPSERT desired_town SET name=spec.name, home=spec.home,
      mayor_model=spec.defaults.mayorModel, polecat_model=spec.defaults.polecatModel,
      max_polecats=spec.defaults.maxPolecats
5. COMMIT → record Dolt commit hash
6. If spec.agents.surveyor = true:
   a. Resolve surveyorClaudeMdRef ConfigMap → get CLAUDE.md content path
   b. Resolve secretsRef Secret → build envFrom reference (not value injection)
   c. Desired Deployment: image=spec.agents.surveyorImage (or operator default),
      volumeMount CLAUDE.md ConfigMap at /gt/CLAUDE.md (read-only),
      envFrom secretsRef, env DOLT_ENDPOINT=DoltInstance.status.endpoint
   d. Create Deployment if missing; update if spec changed
7. Update status.doltCommit, set condition DesiredTopologyInSync=true
8. ObservedGeneration = metadata.generation
```

**Status-sync loop** (30s interval, separate goroutine):
```
SELECT last_reconcile_at, reconcile_health FROM actual_town WHERE name = ?
→ patch status.lastReconcileAt, status.conditions[SurveyorRunning] based on Surveyor pod phase
```

## RigController

**Watches**: Rig, parent GasTown (for defaults and DoltInstance resolution),
            referenced ConfigMaps (mayorClaudeMdRef)

**Reconcile loop**:
```
1. Fetch Rig spec
2. Resolve parent GasTown → DoltInstance readiness gate
3. Apply defaults: if spec.agents.mayorModel == "" → gastownDefaults.mayorModel (etc.)
   (Mutating webhook does this at admission; controller re-applies defensively)
4. Resolve mayorClaudeMdRef ConfigMap → get CLAUDE.md path (for Dolt write)
5. Open Dolt connection
6. BEGIN TRANSACTION
   a. upsertTopologyVersions(tx, {desired_rigs: v, desired_agent_config: v,
                                   desired_formulas: v, desired_rig_custom_roles: v})
   b. UPSERT desired_rigs SET name=spec.name (rig CR name), repo=spec.repo,
      branch=spec.branch, enabled=spec.enabled
   c. UPSERT desired_agent_config SET rig_name=spec.name,
      mayor_enabled=spec.agents.mayor, witness_enabled=spec.agents.witness,
      refinery_enabled=spec.agents.refinery, deacon_enabled=spec.agents.deacon,
      mayor_model=resolvedMayorModel, polecat_model=resolvedPolecatModel,
      max_polecats=resolvedMaxPolecats,
      mayor_claude_md_path="/gt/rigs/<name>/CLAUDE.md"  ← ConfigMap mount path
   d. DELETE desired_formulas WHERE rig_name=spec.name
      INSERT desired_formulas for each spec.formulas entry
   e. DELETE desired_rig_custom_roles WHERE rig_name=spec.name
      INSERT desired_rig_custom_roles for each spec.roles entry (role_name, enabled=true)
7. COMMIT → record Dolt commit hash
8. Update status.doltCommit, set condition DesiredTopologyInSync=true
```

**Status-sync loop** (30s interval):
```
SELECT status, last_seen FROM actual_rigs WHERE name = ?
SELECT COUNT(*) FROM actual_agent_config WHERE rig_name = ? AND role = 'polecat' AND status = 'running'
SELECT last_converged_at FROM reconcile_log WHERE ... (latest successful reconcile for this rig)
→ patch status.runningPolecats, status.rigHealth, status.lastConvergedAt
```

**Deletion handling**: when a Rig CR is deleted, the controller writes
`desired_rigs SET enabled=false` (drain semantics) before removing the row. The Surveyor
reconciles the drain; the controller watches for `actual_rigs.status = 'stopped'` before
completing the deletion. Finalizer: `gastown.io/rig-drain`.

## AgentRoleController

**Watches**: AgentRole, referenced ConfigMaps (claudeMdRef)

**Reconcile loop**:
```
1. Fetch AgentRole spec
2. Resolve parent GasTown → DoltInstance readiness gate
3. Validate name not in reserved list: [mayor, polecat, witness, refinery, deacon, dog, crew]
   (Webhook enforces this; controller validates defensively and emits Event on violation)
4. Open Dolt connection
5. BEGIN TRANSACTION
   a. upsertTopologyVersions(tx, {desired_custom_roles: v, desired_rig_custom_roles: v})
   b. UPSERT desired_custom_roles SET name=metadata.name, scope=spec.scope,
      claude_md_path="/gt/roles/<name>/CLAUDE.md",
      trigger_type=spec.trigger.type,
      trigger_schedule=spec.trigger.schedule (NULL if not schedule type),
      trigger_event=spec.trigger.event (NULL if not event type),
      parent_role=spec.supervision.parent,
      reports_to=spec.supervision.reportsTo,
      max_instances=spec.resources.maxInstances
   (For scope=rig roles: desired_rig_custom_roles junction rows are written by RigController
    when spec.roles includes this role's name)
6. COMMIT
7. Update status.doltCommit, set condition DesiredTopologyInSync=true
```

**Deletion handling**: DELETE from desired_custom_roles. If any desired_rig_custom_roles
rows reference this role, cascade delete them in the same transaction. Finalizer:
`gastown.io/role-cleanup`.

## RBAC — Operator ServiceAccount

```yaml
# ClusterRole for cluster-scoped resources
rules:
- apiGroups: ["gastown.io"]
  resources: ["gastowns", "gastowns/status", "gastowns/finalizers"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]
- apiGroups: ["coordination.k8s.io"]
  resources: ["leases"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]

# Role for namespaced resources (per namespace)
rules:
- apiGroups: ["gastown.io"]
  resources: ["rigs", "rigs/status", "rigs/finalizers",
              "agentroles", "agentroles/status", "agentroles/finalizers",
              "doltinstances", "doltinstances/status"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]
- apiGroups: ["apps"]
  resources: ["statefulsets", "deployments"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]
- apiGroups: [""]
  resources: ["services", "persistentvolumeclaims", "configmaps"]
  verbs: ["get", "list", "watch", "create", "update", "patch"]
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch"]  # read-only — never written by operator
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create", "patch"]
- apiGroups: ["batch"]
  resources: ["jobs"]
  verbs: ["get", "list", "watch", "create", "delete"]  # DDL init/migration jobs only
```

**Explicitly excluded**: `pods` create/delete/update. The operator does not manage Gas Town
agent pods. No `clusterrolebinding` to `cluster-admin`.
