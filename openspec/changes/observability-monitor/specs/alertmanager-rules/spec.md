# Spec: AlertManager PrometheusRule

## Purpose

Six alert rules shipped as a `PrometheusRule` CRD in the Helm chart. They cover the
four operational risk categories: fleet convergence, agent staleness, Beads backlog,
and cost exhaustion. All numeric thresholds are exposed as `values.yaml` defaults so
operators can tune without forking the chart.

## Prerequisites

Requires the Prometheus Operator (part of kube-prometheus-stack) to be installed.
The chart conditionally renders this template:

```yaml
# values.yaml
prometheusRule:
  enabled: false    # set true when Prometheus Operator is present
```

## File Location

```
charts/templates/prometheusrule.yaml
```

## Full PrometheusRule Manifest

```yaml
{{- if .Values.prometheusRule.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: {{ include "gastown-operator.fullname" . }}-alerts
  labels:
    {{- include "gastown-operator.labels" . | nindent 4 }}
    # Standard label Prometheus Operator uses to select rules
    release: {{ .Release.Name }}
spec:
  groups:

  - name: gastown.convergence
    rules:

    # Fleet-wide score below threshold for 5 minutes.
    # Fires before individual rig alerts give context.
    - alert: GasTownConvergenceDegraded
      expr: dgt_fleet_convergence_score_total
              < {{ .Values.prometheusRule.thresholds.convergenceWarning | default 0.9 }}
      for: 5m
      labels:
        severity: warning
      annotations:
        summary: >-
          Gas Town fleet convergence {{ "{{" }} $value | humanizePercentage {{ "}}" }}
          (threshold {{ .Values.prometheusRule.thresholds.convergenceWarning | default 0.9 }})
        runbook: >-
          Check surveyor_escalations_total for recent escalations.
          Run: town-ctl status --dolt-dsn $DSN

    # Any single rig at score 0 for 10 minutes — Mayor completely gone.
    - alert: GasTownRigDown
      expr: dgt_fleet_convergence_score == 0
      for: 10m
      labels:
        severity: critical
      annotations:
        summary: >-
          Rig {{ "{{" }} $labels.rig {{ "}}" }} convergence score is 0
          — Mayor unresponsive for 10m
        runbook: >-
          Mayor for rig {{ "{{" }} $labels.rig {{ "}}" }} may have crashed.
          Check actual_agent_config in Dolt. Deacon should restart it.

  - name: gastown.staleness
    rules:

    # Mayor last_seen expired — process health layer failing.
    - alert: GasTownMayorStale
      expr: dgt_agent_staleness_seconds{role="mayor"}
              > {{ .Values.prometheusRule.thresholds.mayorStalenessSeconds | default 120 }}
      for: 2m
      labels:
        severity: warning
      annotations:
        summary: >-
          Mayor for rig {{ "{{" }} $labels.rig {{ "}}" }} has not reported
          in {{ "{{" }} $value | humanizeDuration {{ "}}" }}
        runbook: >-
          Check actual_agent_config in Dolt for rig {{ "{{" }} $labels.rig {{ "}}" }}.
          Mayor may need manual restart via Deacon.

    # Surveyor itself has not updated convergence score recently.
    # Indicates the Surveyor process may be stuck or crashed.
    - alert: GasTownSurveyorSilent
      expr: time() - surveyor_convergence_score_last_updated_timestamp
              > {{ .Values.prometheusRule.thresholds.surveyorSilenceSeconds | default 300 }}
      for: 0m
      labels:
        severity: warning
      annotations:
        summary: >-
          Surveyor has not completed a reconcile pass in
          {{ "{{" }} $value | humanizeDuration {{ "}}" }}
        runbook: >-
          Check Surveyor Deployment: kubectl get deploy -l app=surveyor.
          Check Surveyor logs for context budget exhaustion or crash.

  - name: gastown.beads
    rules:

    # High-priority operation Beads queuing up — Dogs not processing.
    - alert: GasTownBeadsBacklog
      expr: dgt_beads_open_total{type="task",priority="1"}
              > {{ .Values.prometheusRule.thresholds.beadsBacklogCount | default 10 }}
      for: 10m
      labels:
        severity: warning
      annotations:
        summary: >-
          {{ "{{" }} $value {{ "}}" }} P1 operation Beads open for 10m
          — Dogs may be stuck or overwhelmed
        runbook: >-
          Check bd list --status=in_progress. Check Dog agent health in
          actual_agent_config.

  - name: gastown.cost
    rules:

    # Per-rig budget near exhaustion.
    - alert: GasTownBudgetCritical
      expr: townctl_patrol_budget_pct_used
              > {{ .Values.prometheusRule.thresholds.budgetCriticalPct | default 90 }}
      for: 1m
      labels:
        severity: warning
      annotations:
        summary: >-
          Rig {{ "{{" }} $labels.rig {{ "}}" }} is at
          {{ "{{" }} $value | humanize {{ "}}" }}% of daily budget
        runbook: >-
          Run town-ctl status to see cost breakdown.
          Consider reducing max_polecats for rig or increasing daily_budget_usd.
{{- end }}
```

## values.yaml additions

```yaml
prometheusRule:
  # Set true when Prometheus Operator is installed (kube-prometheus-stack).
  enabled: false

  # Tunable thresholds — override these per environment.
  thresholds:
    # Fleet convergence score below this triggers GasTownConvergenceDegraded.
    convergenceWarning: 0.9

    # Seconds since mayor last_seen before GasTownMayorStale fires.
    # Default is 2× StaleTTL (60s default StaleTTL × 2 = 120s).
    mayorStalenessSeconds: 120

    # Seconds without a Surveyor reconcile pass before GasTownSurveyorSilent fires.
    surveyorSilenceSeconds: 300

    # Open P1 task Beads count before GasTownBeadsBacklog fires.
    beadsBacklogCount: 10

    # Budget % used before GasTownBudgetCritical fires.
    budgetCriticalPct: 90
```

## Alert Decision Rationale

| Alert | Why this threshold | Why this `for` duration |
|-------|--------------------|------------------------|
| `GasTownConvergenceDegraded` | < 0.9 allows one non-critical resource to be lagging | 5m — transient Polecat churn should not page |
| `GasTownRigDown` | score == 0 means Mayor is completely gone | 10m — Deacon restarts Mayor; give it time |
| `GasTownMayorStale` | 120s = 2× StaleTTL; by this point Deacon should have acted | 2m — confirms it is not a heartbeat gap |
| `GasTownSurveyorSilent` | 300s = 5 patrol intervals; Surveyor should have run at least once | 0m — immediate: silence is always abnormal |
| `GasTownBeadsBacklog` | 10 P1 Beads is above normal burst; sustained 10m = structural issue | 10m — prevents noise from temporary spikes |
| `GasTownBudgetCritical` | 90% gives 10% headroom before drain fires | 1m — budget exhaustion is near-instantaneous |

## Non-Goals

- Routing configuration (Slack, PagerDuty, email) — operator responsibility
- Inhibition rules (e.g. suppress rig alerts when fleet alert is firing)
- Recording rules / precomputed aggregations — deferred
- Grafana alert rules (AlertManager is the single alerting source per ADR-0012 D4)
