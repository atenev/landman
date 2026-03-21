## Context

The dgt control plane is operationally blind. The Surveyor — the AI reconciler that
drives fleet convergence — emits no metrics. `actual_topology` in Dolt captures agent
fleet state but nothing aggregates it. Cost patrol effectiveness, Bead workflow timing,
and convergence scores are all invisible until something breaks.

ADR-0011 specifies the observability architecture. This design document details the
implementation of the `observability-monitor` change.

Depends on: `surveyor-topology-reconciler` (actual_topology tables must exist).

## Goals / Non-Goals

**Goals:**
- Design `cmd/dgt-observer`: polling binary with Prometheus endpoint
- Design `pkg/observer`: Dolt reader and Beads workflow reader
- Specify all metric names, labels, and cardinality bounds
- Specify inline metric additions to `pkg/surveyor` and `pkg/townctl`
- Specify K8s Deployment + ServiceMonitor for the observer
- Specify systemd unit for bare metal

**Non-Goals:**
- Distributed tracing / OpenTelemetry — deferred
- Log aggregation — deferred
- Modifying Gas Town agent behavior — prohibited
- Alerting rules / SLOs — left to operator
- Grafana provisioning automation — deferred

## Decisions

### D1: `pkg/observer` is a pure library, `cmd/dgt-observer` is the binary

`pkg/observer` contains: Dolt reader, Beads reader, metric collector structs. It has
no `main()`, no goroutines, no HTTP server. It is importable by the operator if needed.

`cmd/dgt-observer` wires: flag parsing, Dolt connection, Beads Dolt connection, poll
loop goroutine, Prometheus HTTP server, graceful shutdown.

This mirrors the existing pattern: `pkg/townctl` (library) + `cmd/town-ctl` (binary).

### D2: Observer calls `pkg/surveyor.ComputeScore()` directly

The observer imports `pkg/surveyor` and calls `ComputeScore(desired, actual, cfg, now)`
on every poll cycle. This reuses the authoritative scoring function without duplication.

The desired and actual structs are populated by reading Dolt tables. The observer does
not cache: every poll reads current Dolt state.

### D3: Beads read from Dolt `bd_issues` table

The observer connects to Dolt (MySQL protocol) and queries the `bd_issues` table:
- `dgt_beads_open_total`: `SELECT type, priority, COUNT(*) WHERE status IN ('open','in_progress')`
- `dgt_beads_latency_seconds`: `SELECT type, TIMESTAMPDIFF(SECOND, created_at, closed_at) WHERE status='closed' AND closed_at > NOW() - INTERVAL poll_window SECOND`

The poll window for latency samples is `2 × poll_interval` to avoid gaps.

### D4: Poll loop with per-query error isolation

Each metric group (topology, beads, convergence score) is queried independently. A
failure in one group (e.g., `actual_topology` table missing — pre-deployment) must not
prevent other groups from updating. Each group logs errors via `slog` and increments
`dgt_dolt_poll_errors_total{query="<group>"}`, then continues.

### D5: Metrics registered at startup, never unregistered

Prometheus registries do not support safe unregistration under concurrent reads. All
collectors are registered once at binary startup. Gauges for absent resources return 0
(e.g., a rig with no actual agents returns 0 for staleness — zero is meaningful: it means
the agent has never reported in).

### D6: `pkg/surveyor` metrics registered via `sync.Once`

`pkg/surveyor` is a library. Metric registration must be idempotent if multiple tests
or callers create multiple `Verifier` instances. Use `sync.Once` in a package-level
`init()` or `RegisterMetrics()` function. Tests call `RegisterMetrics()` once.

## Component Specifications

### `pkg/observer/dolt.go` — Dolt topology reader

```go
// TopologySnapshot holds a point-in-time read of desired + actual topology from Dolt.
type TopologySnapshot struct {
    Desired surveyor.DesiredTopology
    Actual  surveyor.ActualTopology
    ReadAt  time.Time
}

// ReadTopology queries desired_* and actual_* tables from Dolt.
func ReadTopology(ctx context.Context, db *sql.DB) (TopologySnapshot, error)
```

### `pkg/observer/beads.go` — Beads workflow reader

```go
// BeadsSnapshot holds Beads state relevant to monitoring.
type BeadsSnapshot struct {
    OpenByTypePriority map[BeadsKey]int64  // (type, priority) → count
    RecentLatencies    []LatencySample     // (type, seconds) for histogram updates
    ReadAt             time.Time
}

type BeadsKey struct { Type string; Priority int }
type LatencySample struct { Type string; Seconds float64 }

// ReadBeads queries bd_issues for open counts and recent closed latencies.
func ReadBeads(ctx context.Context, db *sql.DB, window time.Duration) (BeadsSnapshot, error)
```

### `pkg/observer/metrics.go` — Prometheus collectors

All Prometheus metrics defined here. Exported registration function:

```go
// RegisterMetrics registers all dgt-observer metrics with the given registry.
// Safe to call multiple times (idempotent via Already*Registered error handling).
func RegisterMetrics(reg prometheus.Registerer) error
```

### `cmd/dgt-observer/main.go` — Binary entrypoint

Flags:
- `--dolt-dsn`: Dolt MySQL DSN (default: env `DGT_DOLT_DSN`)
- `--interval`: poll interval (default: `15s`)
- `--metrics-addr`: HTTP listen address for /metrics (default: `:9091`)
- `--log-level`: `debug` | `info` | `warn` (default: `info`)

Loop:
1. Connect to Dolt with retry (exponential backoff, max 5 attempts)
2. Register metrics
3. Start HTTP server (`/metrics`, `/healthz`)
4. Poll loop: every `interval`, call `ReadTopology` + `ReadBeads`, update all gauges/counters/histograms
5. On SIGTERM/SIGINT: drain HTTP, close Dolt connection, exit 0

### `pkg/surveyor/metrics.go` — Inline reconciler metrics

Added to the `pkg/surveyor` package. Initialized via `RegisterMetrics(reg)`.

Reconciler records:
- `surveyor_reconcile_total.WithLabelValues(outcome).Inc()` at end of each reconcile
- `surveyor_convergence_score.Set(result.Score)` after each `ComputeScore` call
- `surveyor_verify_retries.Observe(float64(outcome.Attempts))` on reconcile completion
- `surveyor_escalations_total.WithLabelValues(string(reason)).Inc()` on escalation
- `surveyor_reconcile_duration_seconds.WithLabelValues(outcome).Observe(elapsed.Seconds())`

### `pkg/townctl/patrol_metrics.go` — Cost patrol inline metrics

Added to `patrol.go` logic. Records:
- `townctl_patrol_runs_total.WithLabelValues(rigName).Inc()` per rig per patrol run
- `townctl_patrol_actions_total.WithLabelValues(string(action), rigName).Inc()` when action != PatrolNone
- `townctl_patrol_budget_pct_used.WithLabelValues(rigName, budgetType).Set(pctUsed)`
- `townctl_ledger_write_duration_seconds.Observe(writeElapsed.Seconds())`

## K8s Deployment

### observer Deployment

```yaml
# charts/templates/observer-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dgt-observer
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dgt-observer
  template:
    spec:
      serviceAccountName: dgt-observer  # no K8s API access needed
      containers:
      - name: observer
        image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
        args: ["dgt-observer"]
        ports:
        - name: metrics
          containerPort: 9091
        env:
        - name: DGT_DOLT_DSN
          valueFrom:
            secretKeyRef:
              name: dolt-credentials
              key: dsn
        readinessProbe:
          httpGet:
            path: /healthz
            port: 9091
        resources:
          requests: { cpu: 10m, memory: 32Mi }
          limits:   { cpu: 100m, memory: 128Mi }
```

### ServiceMonitor (Prometheus Operator)

```yaml
# charts/templates/observer-servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: dgt-observer
spec:
  selector:
    matchLabels:
      app: dgt-observer
  endpoints:
  - port: metrics
    interval: 30s
    path: /metrics
```

## Bare Metal / systemd

```ini
# /etc/systemd/system/dgt-observer.service
[Unit]
Description=DGT Observer — topology and beads metrics exporter
After=network.target

[Service]
ExecStart=/usr/local/bin/dgt-observer \
  --dolt-dsn=${DGT_DOLT_DSN} \
  --interval=15s \
  --metrics-addr=:9091
Restart=on-failure
RestartSec=5s
EnvironmentFile=/etc/dgt/observer.env

[Install]
WantedBy=multi-user.target
```

## Open Questions

- **actual_topology write protocol**: which Gas Town agent role writes which rows? This
  determines which staleness metrics are meaningful. Pending resolution in
  `surveyor-topology-reconciler` (open question in that change's design.md).
- **Dolt read replica**: for high-frequency setups (>100 agents), should the observer
  connect to a read replica? Defer until we have performance data.
- **Grafana dashboard**: defer to a follow-up task once metric names are stable.
