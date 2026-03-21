## 1. Architecture

- [ ] 1.1 Review, finalise, and accept ADR-0011 (`docs/adr/0011-observability-architecture.md`) [dgt-ytz]
- [ ] 1.2 Review, finalise, and accept ADR-0012 (`docs/adr/0012-observability-presentation-layer.md`) [dgt-ufz]

## 2. pkg/observer — Dolt topology reader

- [ ] 2.1 Implement `pkg/observer/dolt.go` — `ReadTopology(ctx, db) (TopologySnapshot, error)` [dgt-75e]
  - Queries `desired_*` and `actual_*` tables; maps to `surveyor.DesiredTopology` + `surveyor.ActualTopology`
  - Per-query error isolation: failure in one table group does not block others
- [ ] 2.2 Implement `pkg/observer/beads.go` — `ReadBeads(ctx, db, window) (BeadsSnapshot, error)` [dgt-rea]
  - Queries Dolt `bd_issues` table for open counts by (type, priority) and closed latency samples
  - `lastSeenClosedAt` cursor to avoid re-observing the same Bead in the histogram
- [ ] 2.3 Implement `pkg/observer/metrics.go` — Prometheus collector definitions and `RegisterMetrics(reg)` [dgt-exh]
  - See ADR-0011 metric catalogue for all metric names, types, and labels
  - Idempotent registration; no `init()` side effects

## 3. pkg/surveyor — inline reconciler metrics

- [ ] 3.1 Add `pkg/surveyor/metrics.go` with all surveyor_* metrics [dgt-4k8]
  - `surveyor_reconcile_total`, `surveyor_convergence_score`, `surveyor_verify_retries`,
    `surveyor_escalations_total`, `surveyor_reconcile_duration_seconds`,
    `surveyor_stale_branch_cleanups_total`
  - `sync.Once` registration (library safe)
- [ ] 3.2 Wire metric callsites into reconcile state machine (no logic changes) [dgt-4k8]

## 4. pkg/townctl — cost patrol metrics + status command

- [ ] 4.1 Add `pkg/townctl/patrol_metrics.go` with `townctl_patrol_*` and
  `townctl_ledger_write_duration_seconds` metrics [dgt-bp9]
- [ ] 4.2 Wire metric callsites into `patrol.go` (no logic changes) [dgt-bp9]
- [ ] 4.3 Implement `pkg/townctl/status.go` — `StatusOptions`, `Status()`, `StatusResult` [dgt-uyb]
  - Connects via `townctl.ConnectDSN`, reads desired+actual topology, calls `surveyor.ComputeScore`
  - Reads `bd_issues` for open Beads summary; reads cost tables for budget usage
- [ ] 4.4 Implement `pkg/townctl/status_format.go` — `FormatStatusText()` and `FormatStatusJSON()` [dgt-uyb]
  - Text: ANSI-coloured terminal table, auto-disabled on non-TTY stdout
  - JSON: version:1 schema per spec
- [ ] 4.5 Wire `status` subcommand in `cmd/town-ctl/main.go` with exit codes 0/1/2 [dgt-uyb]

## 5. cmd/dgt-observer — observer binary

Depends on: 2.1, 2.2, 2.3

- [ ] 5.1 Implement `cmd/dgt-observer/main.go` — flag parsing, Dolt connect with backoff,
  HTTP server, poll loop, graceful shutdown [dgt-gq2]
- [ ] 5.2 Poll loop: call `ReadTopology` + `ReadBeads` + `pkg/surveyor.ComputeScore`,
  update all collectors [dgt-gq2]
- [ ] 5.3 HTTP server: `/metrics` (Prometheus handler) + `/healthz` (200 OK) [dgt-gq2]

## 6. pkg/k8s/operator — CRD status enrichment

- [ ] 6.1 Add `ConvergenceScore`, `NonConverged`, `LastConvergenceAt` fields to
  `GasTownStatus` in `pkg/k8s/operator/v1alpha1/types.go` [dgt-w9m]
- [ ] 6.2 Add `FleetConverged` and `ActualTopologyAvailable` condition types [dgt-w9m]
- [ ] 6.3 Extend `GasTownReconciler.patchStatusFromActual` to call `readTopologyForStatus()`
  + `surveyor.ComputeScore()`; errors non-fatal; cap NonConverged at 20 [dgt-w9m]

## 7. Deployment

Depends on: 5.1–5.3

- [ ] 7.1 Add observer Deployment, Service, ServiceMonitor, RBAC to `charts/` [dgt-6z5]
- [ ] 7.2 Add `deploy/systemd/dgt-observer.service` unit file for bare metal [dgt-pqf]
- [ ] 7.3 Add `charts/grafana/gastown-fleet-health.json` and `charts/templates/grafana-dashboard-cm.yaml` [dgt-4dc]
  - 5-row dashboard per spec (fleet overview, per-rig timeline, Beads, cost, Surveyor internals)
  - `grafana.enabled=false` default in `values.yaml`
- [ ] 7.4 Add `charts/templates/prometheusrule.yaml` with 6 alert rules [dgt-9vn]
  - All thresholds as `prometheusRule.thresholds.*` in `values.yaml`
  - `prometheusRule.enabled=false` default

## 8. Tests

- [ ] 8.1 Unit tests for `pkg/observer/dolt.go` and metric update logic (fake `*sql.DB`) [dgt-gmm]
- [ ] 8.2 Unit tests for `pkg/observer/beads.go` (fake `*sql.DB`) [dgt-358]
- [ ] 8.3 Unit tests for `pkg/townctl/status.go` and `status_format.go` [dgt-68y]
  - Text output format, JSON schema, exit codes, ANSI colour enable/disable
- [ ] 8.4 Unit tests for CRD status enrichment in `gastown_controller.go` [dgt-r108]
  - ConvergenceScore computation, NonConverged cap, condition types, non-fatal error path
- [ ] 8.5 Integration test for `cmd/dgt-observer` poll loop + HTTP server [dgt-ydh]
