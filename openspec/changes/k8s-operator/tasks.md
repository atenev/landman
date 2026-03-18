## 1. CRD Types and Schema

- [ ] 1.1 Define Go struct types for GasTown, Rig, AgentRole, DoltInstance with kubebuilder markers [dgt-3xu]
- [ ] 1.2 Define shared types: LocalRef, NamespacedRef, status condition helpers [dgt-3xu]
- [ ] 1.3 Run controller-gen to generate CRD YAML and DeepCopy functions [dgt-3xu]
- [ ] 1.4 Validate OpenAPI v3 schema output covers all field constraints in crd-schema/spec.md [dgt-3xu]

## 2. DoltInstance Controller

- [ ] 2.1 Implement DoltInstanceReconciler: StatefulSet create/update [dgt-2ih]
- [ ] 2.2 Implement headless Service and ClusterIP Service reconcile [dgt-2ih]
- [ ] 2.3 Implement PVC reconcile (create only — never resize in place) [dgt-2ih]
- [ ] 2.4 Implement DDL init Job creation on first startup, readiness gate on Job completion [dgt-2ih]
- [ ] 2.5 Implement schema migration Job on spec.version change [dgt-2ih]
- [ ] 2.6 Embed DDL SQL files via go:embed [dgt-2ih]
- [ ] 2.7 Set status.endpoint and status.conditions[Ready, DDLInitialized] [dgt-2ih]

## 3. GasTown Controller

- [ ] 3.1 Implement GasTownReconciler: DoltInstance readiness gate [dgt-6mb]
- [ ] 3.2 Implement upsertTopologyVersions() shared utility (ADR-0003) [dgt-6mb]
- [ ] 3.3 Implement desired_town Dolt write with versions-first transaction [dgt-6mb]
- [ ] 3.4 Implement Surveyor Deployment create/update (CLAUDE.md ConfigMap mount, envFrom secretsRef) [dgt-6mb]
- [ ] 3.5 Implement status-sync loop (30s): actual_town → status patches [dgt-6mb]

## 4. Rig Controller

- [ ] 4.1 Implement RigReconciler: parent GasTown resolution and DoltInstance readiness gate [dgt-d3r]
- [ ] 4.2 Implement desired_rigs, desired_agent_config, desired_formulas, desired_rig_custom_roles write [dgt-d3r]
- [ ] 4.3 Implement drain finalizer: set enabled=false, wait for actual_rigs.status=stopped [dgt-d3r]
- [ ] 4.4 Implement status-sync loop (30s): actual_rigs + actual_agent_config → status patches [dgt-d3r]

## 5. AgentRole Controller

- [ ] 5.1 Implement AgentRoleReconciler: desired_custom_roles write [dgt-kp4]
- [ ] 5.2 Implement cleanup finalizer: cascade-delete desired_rig_custom_roles rows [dgt-kp4]
- [ ] 5.3 Implement status-sync loop: actual_custom_roles → status patches [dgt-kp4]

## 6. Admission Webhooks

- [ ] 6.1 Implement ValidatingWebhook for AgentRole: 4 rules (reserved name, parent required, self-supervision, trigger consistency) [dgt-tqw]
- [ ] 6.2 Implement ValidatingWebhook for Rig: townRef existence check [dgt-tqw]
- [ ] 6.3 Implement ValidatingWebhook for GasTown: doltRef existence, surveyor requires claudeMdRef [dgt-tqw]
- [ ] 6.4 Implement ValidatingWebhook for DoltInstance: replicas == 1 [dgt-tqw]
- [ ] 6.5 Implement MutatingWebhook for Rig: default mayorModel, polecatModel, maxPolecats from parent GasTown [dgt-tqw]
- [ ] 6.6 Wire cert-manager Certificate for webhook TLS [dgt-tqw]
- [ ] 6.7 Implement --webhook-tls-cert-file / --webhook-tls-key-file manual TLS flags [dgt-tqw]

## 7. RBAC

- [ ] 7.1 Define ClusterRole for GasTown (cluster-scoped) and lease management [dgt-a92]
- [ ] 7.2 Define Role for namespaced resources (Rig, AgentRole, DoltInstance, apps, Services, PVCs, Secrets read-only) [dgt-a92]
- [ ] 7.3 Verify no Pod create/update/delete permissions exist [dgt-a92]

## 8. Helm Chart

- [ ] 8.1 Scaffold Helm chart: Chart.yaml, values.yaml, templates/ [dgt-4ku]
- [ ] 8.2 Template: operator Deployment (image, resources, flags) [dgt-4ku]
- [ ] 8.3 Template: ServiceAccount + ClusterRoleBinding + RoleBinding [dgt-4ku]
- [ ] 8.4 Template: webhook Service + cert-manager Certificate [dgt-4ku]
- [ ] 8.5 Template: ValidatingWebhookConfiguration + MutatingWebhookConfiguration with caBundle injection [dgt-4ku]
- [ ] 8.6 CRDs in crds/ directory (installed by Helm before templates) [dgt-4ku]
- [ ] 8.7 Document cert-manager.enabled flag and manual TLS alternative in values.yaml [dgt-4ku]

## 9. Integration Tests

- [ ] 9.1 envtest setup: start API server + etcd, install CRDs, start operator [dgt-1ba]
- [ ] 9.2 Test: DoltInstance → StatefulSet created, DDL init Job runs, status.Ready=true [dgt-1ba]
- [ ] 9.3 Test: GasTown apply → desired_town row in Dolt, Surveyor Deployment created [dgt-1ba]
- [ ] 9.4 Test: Rig apply → desired_rigs + desired_agent_config rows, defaults applied [dgt-1ba]
- [ ] 9.5 Test: Rig delete → drain finalizer fires, desired_rigs.enabled=false [dgt-1ba]
- [ ] 9.6 Test: AgentRole with name="mayor" → webhook denies [dgt-1ba]
- [ ] 9.7 Test: DoltInstance with replicas=2 → webhook denies [dgt-1ba]
- [ ] 9.8 Test: AgentRole with empty supervision.parent → webhook denies [dgt-1ba]
- [ ] 9.9 Test: desired_topology_versions row written first in every Dolt transaction [dgt-1ba]
