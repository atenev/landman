# Spec: Grafana Dashboard

## Purpose

A single versioned Grafana dashboard that gives platform operators a unified view
of fleet health, Beads workflow performance, cost burn, and Surveyor internals.
Shipped as a static JSON file in the Helm chart. No code generation required.

## File Location

```
charts/grafana/
└── gastown-fleet-health.json     dashboard definition (Grafana JSON model)

charts/templates/
└── grafana-dashboard-cm.yaml     ConfigMap that mounts the JSON for auto-provisioning
```

`grafana.enabled` in `values.yaml` controls whether the ConfigMap is rendered.
Default: `false` — teams with existing Grafana installations import the JSON manually.

## Data Sources

The dashboard requires two Prometheus data sources, both pointing at the same
Prometheus instance that scrapes all three ServiceMonitors:

| Source | Metrics prefix | ServiceMonitor |
|--------|---------------|----------------|
| `dgt-observer` | `dgt_*` | observer-servicemonitor |
| `gastown-operator` | `gastown_operator_*`, `townctl_*` | operator-servicemonitor |
| `surveyor` | `surveyor_*` | surveyor-servicemonitor |

A single Prometheus data source labeled `gastown` is sufficient if all three scrape
targets are in the same Prometheus. The dashboard uses `${datasource}` variable.

## Dashboard Variables (Grafana template variables)

| Variable | Query | Default |
|----------|-------|---------|
| `$datasource` | data source picker | — |
| `$rig` | `label_values(dgt_fleet_convergence_score, rig)` | All |
| `$interval` | interval options: 1m, 5m, 15m | 5m |

## Panel Layout (5 rows)

### Row 1 — Fleet Overview (stat panels, refresh: 15s)

Three stat panels side by side:

**Fleet Convergence Score**
```
Query:  dgt_fleet_convergence_score_total
Type:   Stat
Unit:   percentunit  (0–1 → 0%–100%)
Thresholds:
  green  >= 1.0
  yellow >= 0.9
  red    < 0.9
```

**Converged Rigs**
```
Query:  count(dgt_fleet_convergence_score == 1) OR vector(0)
        /
        count(dgt_fleet_convergence_score >= 0) OR vector(1)
Type:   Stat (fraction, e.g. "3 / 4")
```

**Stale Agents**
```
Query:  count(dgt_agent_staleness_seconds > 60) OR vector(0)
Type:   Stat
Thresholds: green=0, yellow=1, red=3
```

### Row 2 — Per-Rig Convergence (time series, last 1h)

**Convergence Score by Rig**
```
Query:  dgt_fleet_convergence_score{rig=~"$rig"}
Type:   Time series
Legend: {{rig}}
Y-axis: 0–1, unit: short
Threshold band: 0.9 (dashed yellow), 1.0 (solid green)
```

**Pool Size Delta by Rig**
```
Query:  dgt_pool_size_delta{rig=~"$rig"}
Type:   Time series
Legend: {{rig}}
Y-axis: unit: short  (positive = under-provisioned)
```

### Row 3 — Beads Workflow

**Beads Queue Depth**
```
Query:  dgt_beads_open_total
Type:   Bar gauge (stacked by type+priority)
Legend: {{type}} P{{priority}}
```

**Beads Filed-to-Closed Latency (heatmap)**
```
Query:  rate(dgt_beads_latency_seconds_bucket{type="task"}[$interval])
Type:   Heatmap
Y-axis: seconds (log scale)
Color:  green (fast) → red (slow > 300s)
```

**Beads Throughput**
```
Query:  rate(dgt_beads_closed_total[$interval])
Type:   Time series, stacked by type
```

### Row 4 — Cost Patrol

**Budget Usage by Rig**
```
Query:  townctl_patrol_budget_pct_used
Type:   Gauge (one per rig using repeat panel)
Repeat: by rig label
Unit:   percent (0–100)
Thresholds:
  green  < warn_at_pct (default 80)
  yellow < 100
  red    >= 100
```

**Patrol Actions Rate**
```
Query:  rate(townctl_patrol_actions_total[$interval])
Type:   Time series, stacked by action (warn/drain)
Legend: {{action}} — {{rig}}
```

### Row 5 — Surveyor Internals (collapsed by default)

**Reconcile Outcomes**
```
Query:  rate(surveyor_reconcile_total[$interval])
Type:   Time series, stacked by outcome (success/escalated/abandoned)
```

**Escalation Rate**
```
Query:  rate(surveyor_escalations_total[$interval])
Type:   Time series by reason
Thresholds: alert annotation at > 0
```

**Verify Retries Distribution**
```
Query:  histogram_quantile(0.50, rate(surveyor_verify_retries_bucket[$interval]))
        histogram_quantile(0.99, rate(surveyor_verify_retries_bucket[$interval]))
Type:   Time series (p50, p99)
```

**Convergence Score — Surveyor vs Observer**
```
Query A:  surveyor_convergence_score             (label: "surveyor (last reconcile)")
Query B:  dgt_fleet_convergence_score_total      (label: "observer (continuous)")
Type:   Time series
Note:   When A < B: Surveyor's last view was worse than current — likely recovered.
        When A > B: Surveyor saw a better state than now — fleet degraded since last reconcile.
```

## ConfigMap for Auto-Provisioning

```yaml
# charts/templates/grafana-dashboard-cm.yaml
{{- if .Values.grafana.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: gastown-fleet-health-dashboard
  labels:
    grafana_dashboard: "1"    # label watched by Grafana sidecar provisioner
data:
  gastown-fleet-health.json: |
    {{ .Files.Get "grafana/gastown-fleet-health.json" | indent 4 }}
{{- end }}
```

The `grafana_dashboard: "1"` label is the standard convention for Grafana's sidecar
dashboard provisioner (used in kube-prometheus-stack). No Grafana configuration changes
needed if the sidecar is already running.

## values.yaml additions

```yaml
grafana:
  # Set to true if grafana sidecar provisioner is running in the cluster.
  # Creates a ConfigMap with the gastown-fleet-health dashboard JSON.
  enabled: false
```

## Non-Goals

- Grafana alert rules (use Prometheus AlertManager rules instead — single source of truth)
- Per-agent panel (too high cardinality for a dashboard; use `town-ctl status` instead)
- Loki log panel integration
- Dashboard folder / UID management (set by the importing operator)
