# Spec: CRD Status Enrichment

## Purpose

Enrich `GasTownStatus` with convergence data so that `kubectl describe gastown` and
`kubectl get gastown -o yaml` surface fleet health without requiring access to
Prometheus or `town-ctl`. The operator already polls Dolt every 30s
(`GasTownReconciler.StartStatusSync`); this spec extends that existing loop.

## Fields Added to GasTownStatus

```go
// GasTownStatus defines the observed state of a GasTown.
type GasTownStatus struct {
    // existing fields
    Conditions          []metav1.Condition `json:"conditions,omitempty"`
    DoltCommit          string             `json:"doltCommit,omitempty"`
    ObservedGeneration  int64              `json:"observedGeneration,omitempty"`
    LastReconcileAt     *metav1.Time       `json:"lastReconcileAt,omitempty"`

    // NEW: Convergence score in [0.0, 1.0] computed by the status-sync loop.
    // Updated every statusSyncInterval (default 30s). A value of 1.0 means
    // all desired resources are running and fresh. Zero means the fleet is
    // entirely unconverged. -1 means no actual_topology data available yet.
    // +kubebuilder:validation:Minimum=-1
    // +kubebuilder:validation:Maximum=1
    ConvergenceScore float64 `json:"convergenceScore,omitempty"`

    // NEW: Human-readable list of non-converged resources from the last
    // status-sync. Mirrors ScoreResult.NonConverged. Maximum 20 entries
    // to bound CRD object size. Empty when ConvergenceScore == 1.0.
    // +kubebuilder:validation:MaxItems=20
    NonConverged []string `json:"nonConverged,omitempty"`

    // NEW: Timestamp of the last successful convergence score computation.
    // Nil if no actual_topology data has been read yet.
    LastConvergenceAt *metav1.Time `json:"lastConvergenceAt,omitempty"`
}
```

## Implementation: Extending patchStatusFromActual

`gastown_controller.go:patchStatusFromActual` currently reads `actual_town.last_reconcile_at`.
Extend it to also:

1. Read all `desired_*` and `actual_*` tables (same queries as `pkg/observer/dolt.go`
   `ReadTopology` — extract a shared internal helper or duplicate inline initially)
2. Call `surveyor.ComputeScore(desired, actual, surveyor.DefaultProductionConfig(), time.Now())`
3. Write `ConvergenceScore`, `NonConverged` (capped at 20), `LastConvergenceAt` to status

```go
func (r *GasTownReconciler) patchStatusFromActual(
    ctx context.Context,
    dolt *doltClient,
    gt *gasv1alpha1.GasTown,
) error {
    // existing: read last_reconcile_at
    // ...

    // new: compute convergence score
    desired, actual, err := readTopologyForStatus(ctx, dolt.db, gt.Name)
    if err != nil {
        // non-fatal: log and continue with existing status fields
        log.FromContext(ctx).Info("convergence score unavailable", "reason", err.Error())
    } else {
        result := surveyor.ComputeScore(desired, actual,
            surveyor.DefaultProductionConfig(), time.Now())
        gt.Status.ConvergenceScore = result.Score
        gt.Status.NonConverged = cappedSlice(result.NonConverged, 20)
        t := metav1.Now()
        gt.Status.LastConvergenceAt = &t
    }
    // existing: Status().Update(ctx, gt)
}
```

## kubectl Output

After this change, `kubectl describe gastown my-town` shows:

```
Status:
  Conditions:
    Type:    DesiredTopologyInSync   Status: True   Reason: Synced
    Type:    SurveyorRunning         Status: True   Reason: DeploymentReady
  Convergence Score:    0.94
  Non Converged:
    rig/legacy: not converged
    pool/legacy: not converged
  Last Convergence At:  2026-03-21T14:22:31Z
  Last Reconcile At:    2026-03-21T14:22:01Z
  Dolt Commit:          abc1234f
```

And `kubectl get gastown` can use a custom column:
```bash
kubectl get gastown -o custom-columns=\
  NAME:.metadata.name,\
  SCORE:.status.convergenceScore,\
  STALE:.status.nonConverged
```

## Condition Additions

Two new condition types on `GasTownStatus.Conditions`:

| Type | True | False |
|------|------|-------|
| `FleetConverged` | score == 1.0 | score < 1.0 (Reason: `PartialConvergence`, Message: non-converged list) |
| `ActualTopologyAvailable` | actual_* tables readable | tables missing/empty (Reason: `SurveyorNotStarted`) |

These are set inside `patchStatusFromActual` using the existing `r.setCondition` helper.

## Cardinality / Size Constraints

- `NonConverged` capped at 20 entries (prevents unbounded CRD object growth with large
  fleets). If more than 20 resources are non-converged, append `"... and N more"`.
- `ConvergenceScore` is a float64; stored in etcd as a JSON number (~18 bytes).
- No per-agent fields in CRD status — those are in `actual_topology` Dolt tables.
  The CRD status is a summary, not a full inventory.

## Non-Goals

- Per-rig status in `GasTownStatus` — Rig CRs already have their own status
- Real-time updates (30s lag is acceptable per ADR-0012 D2)
- Writing convergence score back to Dolt
