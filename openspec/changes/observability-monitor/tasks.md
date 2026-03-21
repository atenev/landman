## 1. Architecture

- [ ] 1.1 Review, finalise, and accept ADR-0011 (`docs/adr/0011-observability-architecture.md`) [dgt-ytz]

## 2. pkg/observer — Dolt topology reader

- [ ] 2.1 Implement `pkg/observer/dolt.go` — `ReadTopology(ctx, db) (TopologySnapshot, error)` [dgt-75e]
  - Queries `desired_*` and `actual_*` tables; maps to `surveyor.DesiredTopology` + `surveyor.ActualTopology`
  - Per-query error isolation: failure in one table group does not block others
- [ ] 2.2 Implement `pkg/observer/beads.go` — `ReadBeads(ctx, db, window) (BeadsSnapshot, error)` [dgt-rea]
  - Queries Dolt `bd_issues` table for open counts by (type, priority) and closed latency samples
- [ ] 2.3 Implement `pkg/observer/metrics.go` — Prometheus collector definitions and `RegisterMetrics(reg)` [dgt-exh]
  - See ADR-0011 metric catalogue for all metric names, types, and labels
  - Idempotent registration (safe if called multiple times)

## 3. pkg/surveyor — inline reconciler metrics

- [ ] 3.1 Add `pkg/surveyor/metrics.go` with `surveyor_reconcile_total`, `surveyor_convergence_score`,
  `surveyor_verify_retries`, `surveyor_escalations_total`, `surveyor_reconcile_duration_seconds`,
  `surveyor_stale_branch_cleanups_total` [dgt-4k8]
- [ ] 3.2 Wire metric callsites into reconcile state machine (no logic changes) [dgt-4k8]

## 4. pkg/townctl — cost patrol metrics

- [ ] 4.1 Add `pkg/townctl/patrol_metrics.go` with `townctl_patrol_runs_total`,
  `townctl_patrol_actions_total`, `townctl_patrol_budget_pct_used`,
  `townctl_ledger_write_duration_seconds` [dgt-bp9]
- [ ] 4.2 Wire metric callsites into `patrol.go` (no logic changes) [dgt-bp9]

## 5. cmd/dgt-observer — observer binary

Depends on: 2.1, 2.2, 2.3

- [ ] 5.1 Implement `cmd/dgt-observer/main.go` — flag parsing, Dolt connect with backoff, HTTP server,
  poll loop, graceful shutdown [dgt-gq2]
- [ ] 5.2 Poll loop: call `ReadTopology` + `ReadBeads` + `pkg/surveyor.ComputeScore`, update all collectors [dgt-gq2]
- [ ] 5.3 HTTP server: `/metrics` (Prometheus handler) + `/healthz` (200 OK) [dgt-gq2]

## 6. Deployment

Depends on: 5.1–5.3

- [ ] 6.1 Add `charts/templates/observer-deployment.yaml` + `observer-service.yaml` +
  `observer-servicemonitor.yaml` + `observer-rbac.yaml` to Helm chart [dgt-6z5]
- [ ] 6.2 Add `deploy/systemd/dgt-observer.service` unit file for bare metal [dgt-pqf]

## 7. Tests

- [ ] 7.1 Unit tests for `pkg/observer/dolt.go` and metric update logic (fake `*sql.DB`) [dgt-gmm]
- [ ] 7.2 Unit tests for `pkg/observer/beads.go` (fake `*sql.DB`) [dgt-358]
- [ ] 7.3 Integration test for `cmd/dgt-observer` poll loop + HTTP server [dgt-ydh]
