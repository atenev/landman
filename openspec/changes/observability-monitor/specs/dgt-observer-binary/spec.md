# Spec: dgt-observer Binary

## Purpose

`dgt-observer` is a read-only Go binary that runs alongside the operator or `town-ctl`.
It polls Dolt at a fixed interval, reads `desired_topology`, `actual_topology`, and
Beads state, and exposes a Prometheus `/metrics` endpoint. It is the canonical source
for fleet health metrics not available from the operator or Surveyor.

## Package Layout

```
cmd/dgt-observer/
  main.go          — entrypoint, flag parsing, wiring
pkg/observer/
  dolt.go          — Dolt topology reader
  beads.go         — Beads workflow reader (reads bd_issues from Dolt)
  metrics.go       — Prometheus metric definitions and RegisterMetrics()
```

## CLI Flags

| Flag | Default | Env override | Description |
|------|---------|--------------|-------------|
| `--dolt-dsn` | — | `DGT_DOLT_DSN` | Dolt MySQL DSN (required) |
| `--interval` | `15s` | `DGT_OBSERVER_INTERVAL` | Poll interval |
| `--metrics-addr` | `:9091` | `DGT_OBSERVER_METRICS_ADDR` | HTTP listen address |
| `--log-level` | `info` | — | Logging level: debug/info/warn |

## Startup Sequence

1. Parse flags; fail fast if `--dolt-dsn` is empty
2. Connect to Dolt: `sql.Open("mysql", dsn)` + `db.PingContext(ctx)` with exponential
   backoff (base 1s, factor 2, max 5 attempts, jitter). Exit 1 if all attempts fail.
3. Call `observer.RegisterMetrics(prometheus.DefaultRegisterer)`
4. Start HTTP server in a goroutine: `/metrics` → `promhttp.Handler()`, `/healthz` → 200 OK
5. Start poll loop in a goroutine (see Poll Loop)
6. Block on SIGTERM/SIGINT; on signal: cancel context, drain HTTP (5s timeout), close DB

## Poll Loop

On each tick:
1. Call `observer.ReadTopology(ctx, db)` → `TopologySnapshot`
2. Call `observer.ReadBeads(ctx, db, 2*interval)` → `BeadsSnapshot`
3. For each rig in snapshot:
   - `dgt_fleet_convergence_score.WithLabelValues(rig).Set(score)` using `pkg/surveyor.ComputeScore`
   - Update `dgt_agent_staleness_seconds`, `dgt_pool_size_*`, `dgt_worktrees_total`
4. Update Beads gauges from `BeadsSnapshot.OpenByTypePriority`
5. Append latency observations from `BeadsSnapshot.RecentLatencies` to histogram
6. Record poll duration in `dgt_dolt_poll_duration_seconds`

Per-group error isolation: if step 1 fails, log + increment `dgt_dolt_poll_errors_total`
and continue to step 2. Never skip an entire poll on partial failure.

## HTTP Endpoints

| Path | Method | Response |
|------|--------|----------|
| `/metrics` | GET | Prometheus text exposition format |
| `/healthz` | GET | `200 OK` with body `"ok"` |

## Resource Requirements (K8s)

- CPU request: 10m, limit: 100m
- Memory request: 32Mi, limit: 128Mi
- No K8s API access (no ServiceAccount token mount needed)
- Dolt access: read-only MySQL user with SELECT on `desired_*`, `actual_*`, `bd_issues`

## Non-Goals

- No write access to Dolt (read-only)
- No Beads creation
- No LLM calls
- No controller-runtime dependency
