# Spec: Admission Webhooks

## Overview

Two admission webhooks enforce ADR invariants at the Kubernetes API layer. Violations are
rejected before any controller reconcile loop runs.

TLS for the webhook server is managed by cert-manager. The operator Helm chart declares
a cert-manager `Certificate` resource and injects the CA bundle into the
`ValidatingWebhookConfiguration` and `MutatingWebhookConfiguration` via a cert-manager
annotation.

## Validating Webhook

**Resource rules**:

### AgentRole validation

```
Rule 1: name not in reserved built-in list (ADR-0004 D4)
  reserved := ["mayor", "polecat", "witness", "refinery", "deacon", "dog", "crew"]
  if agentRole.Name in reserved:
    deny: "spec.metadata.name: %q is a reserved built-in Gas Town role name and cannot be
           used for a custom role; choose a different name (ADR-0004)"

Rule 2: supervision.parent is non-empty (ADR-0004 D3)
  if agentRole.Spec.Supervision.Parent == "":
    deny: "spec.supervision.parent: required field; every custom role must declare a
           supervision relationship (ADR-0004)"

Rule 3: supervision.parent is not self-referential
  if agentRole.Spec.Supervision.Parent == agentRole.Name:
    deny: "spec.supervision.parent: %q cannot supervise itself"

Rule 4: trigger field consistency (ADR-0004 D6)
  if agentRole.Spec.Trigger.Type == "schedule" && agentRole.Spec.Trigger.Schedule == "":
    deny: "spec.trigger.schedule: required when spec.trigger.type is 'schedule'"
  if agentRole.Spec.Trigger.Type == "event" && agentRole.Spec.Trigger.Event == "":
    deny: "spec.trigger.event: required when spec.trigger.type is 'event'"
```

### Rig validation

```
Rule 5: townRef resolves to an existing GasTown
  gastowns := list GasTown (cluster-scoped)
  if rig.Spec.TownRef not in gastowns:
    deny: "spec.townRef: GasTown %q not found in cluster"
```

### GasTown validation

```
Rule 6: doltRef resolves to an existing DoltInstance
  doltInstances := list DoltInstance in namespace rig.Spec.DoltRef.Namespace
  if gastownSpec.DoltRef.Name not found:
    deny: "spec.doltRef: DoltInstance %q not found in namespace %q"

Rule 7: surveyor=true requires surveyorClaudeMdRef
  if gastownSpec.Agents.Surveyor == true && gastownSpec.Agents.SurveyorClaudeMdRef == nil:
    deny: "spec.agents.surveyorClaudeMdRef: required when spec.agents.surveyor is true"
```

### DoltInstance validation

```
Rule 8: replicas == 1 (MVP constraint, ADR-0005 D4)
  if doltInstance.Spec.Replicas > 1:
    deny: "spec.replicas: Invalid value: %d: replicas > 1 not supported in
           gastown-operator v0.x"
```

## Mutating Webhook

Applies defaults from parent GasTown to Rig at admission time. This ensures Rig objects
in the cluster always have resolved, non-empty model and capacity fields — controllers
do not need to re-resolve defaults at reconcile time.

### Rig defaulting

```
Mutation 1: mayorModel default
  if rig.Spec.Agents.MayorModel == "":
    gastownDefaults = fetch GasTown(rig.Spec.TownRef).Spec.Defaults
    rig.Spec.Agents.MayorModel = gastownDefaults.MayorModel

Mutation 2: polecatModel default
  if rig.Spec.Agents.PolecatModel == "":
    rig.Spec.Agents.PolecatModel = gastownDefaults.PolecatModel

Mutation 3: maxPolecats default
  if rig.Spec.Agents.MaxPolecats == 0:
    rig.Spec.Agents.MaxPolecats = gastownDefaults.MaxPolecats
```

If the parent GasTown cannot be fetched at admission time (e.g., GasTown not yet created),
the mutating webhook allows the Rig through without defaulting and logs a warning. The
validating webhook's Rule 5 will catch the missing townRef on the same request and deny it.
The mutating webhook runs before the validating webhook, so this ordering is safe.

## Webhook Server Configuration

```go
// Webhook server setup in main.go
mgr.AddWebhookFor(&v1alpha1.AgentRole{})  // validates + defaults
mgr.AddWebhookFor(&v1alpha1.Rig{})        // validates + defaults
mgr.AddWebhookFor(&v1alpha1.GasTown{})    // validates
mgr.AddWebhookFor(&v1alpha1.DoltInstance{}) // validates
```

**WebhookConfiguration resources** (cert-manager injects caBundle):
```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: gastown-validating-webhook
  annotations:
    cert-manager.io/inject-ca-from: gastown-system/gastown-operator-webhook-cert
webhooks:
- name: vagentrole.gastown.io
  rules:
  - apiGroups: ["gastown.io"]
    apiVersions: ["v1alpha1"]
    resources: ["agentroles"]
    operations: ["CREATE", "UPDATE"]
  failurePolicy: Fail   # deny on webhook unavailability — safety over availability
  sideEffects: None
  admissionReviewVersions: ["v1"]
# ... (similar entries for rig, gastowns, doltinstances)
```

`failurePolicy: Fail` is intentional: the ADR invariants (no shadowed built-in roles, no
unsupervised custom roles) are correctness constraints, not soft recommendations. Prefer
rejecting applies over silently creating invalid objects.

## cert-manager Dependency

The operator Helm chart requires cert-manager to be installed in the cluster:
```yaml
# Chart.yaml
dependencies:
- name: cert-manager
  version: ">=1.13.0"
  repository: https://charts.jetstack.io
  condition: cert-manager.enabled  # can disable if cert-manager already installed
```

For environments that cannot use cert-manager, the operator supports manual TLS via:
```
--webhook-tls-cert-file /path/to/tls.crt
--webhook-tls-key-file  /path/to/tls.key
```
The CA bundle must then be manually injected into the WebhookConfiguration.
