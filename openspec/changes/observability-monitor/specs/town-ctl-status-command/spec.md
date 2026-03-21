# Spec: town-ctl status Command

## Purpose

A new `town-ctl status` subcommand that gives an operator an immediate, readable
summary of fleet health. Works with a direct Dolt connection — no Prometheus, no
observer binary, no Kubernetes required.

## Why a Separate Subcommand (not town-ctl apply output)

`town-ctl apply` writes desired state. `town-ctl status` reads actual state. They
answer different questions and run at different times. Coupling them would mean
`apply` must connect to `actual_topology` tables, which do not exist until the
Surveyor has run at least once.

## Interface

```
Usage:
  town-ctl status [flags]

Flags:
  --dolt-dsn    string   Dolt MySQL DSN (env: GT_DOLT_DSN)
  --output      string   Output format: text (default) or json
  --rig         string   Filter to a single rig name (repeatable)
  --no-color             Disable ANSI colour codes
```

## Data Sources (all from Dolt, no observer dependency)

| Data | Source tables |
|------|--------------|
| Per-rig convergence score | `desired_*` + `actual_*` via `surveyor.ComputeScore` |
| Pool size (desired/actual) | `desired_agent_config`, `actual_agent_config` |
| Agent staleness | `actual_agent_config.last_seen` |
| Custom role health | `actual_custom_roles` |
| Open Beads summary | `bd_issues` WHERE status IN ('open','in_progress') |
| Budget usage | `desired_cost_policy` JOIN `cost_ledger_24h` |

## Text Output Format

```
TOWN: <name>   score=<N>   <status-icon>   (<age> ago)

RIG          STATUS    MAYOR    POLECATS   SCORE   STALE
<rig>        running   healthy  7 / 8      1.00    —
<rig>        running   STALE    0 / 4      0.00    mayor: 127s
<rig>        stopped   —        0 / 0      1.00    —

CUSTOM ROLES
  <rig>/<role>[<idx>]   running   ok
  <rig>/<role>[<idx>]   STALE     last seen: 95s ago

OPEN BEADS
  task  P1   4 open    oldest: 3m
  task  P0   1 open  ← ESCALATION: score=0.00 reason=dog-failure

COST (24h)
  <rig>    $12.40 / $50.00   24%
  <rig>    $48.20 / $50.00   96% ⚠
```

**Status icons** (fleet-level, header line):
- `✓` — score == 1.0, no stale agents
- `⚠` — score < 1.0 or any agent stale > StaleTTL
- `✗` — score == 0.0 for any rig

**Colour rules** (TTY only, auto-disabled on non-TTY stdout):
- Score `< 0.9`: yellow
- Score `== 0.0`: red
- STALE label: red
- Cost `> 90%`: yellow; `> 100%`: red
- Escalation Beads row: red

**Column widths**: fixed at 12/10/9/10/7/8 characters. Longer rig names truncate
with `…`. All widths are constants, not dynamic — avoids a Dolt query just for layout.

## JSON Output Format (`--output=json`)

```json
{
  "version": 1,
  "town": "my-town",
  "score": 0.94,
  "read_at": "2026-03-21T14:22:01Z",
  "rigs": [
    {
      "name": "main",
      "status": "running",
      "score": 1.0,
      "mayor_stale_seconds": 0,
      "pool_desired": 8,
      "pool_actual": 7,
      "non_converged": []
    }
  ],
  "custom_roles": [
    {
      "rig": "main",
      "role": "code-reviewer",
      "instance": 0,
      "status": "running",
      "stale_seconds": 0
    }
  ],
  "open_beads": [
    { "type": "task", "priority": 1, "count": 4, "oldest_seconds": 180 }
  ],
  "cost": [
    { "rig": "main", "spend_usd": 12.40, "budget_usd": 50.0, "pct": 24.8 }
  ]
}
```

`version: 1` allows breaking schema changes in future without surprising scripts.

## Implementation Location

New file: `pkg/townctl/status.go`
- `StatusOptions` struct (mirrors `ApplyOptions` pattern)
- `Status(file string, opts StatusOptions) (*StatusResult, error)`
  - Connects via `townctl.ConnectDSN`
  - Calls `surveyor.ComputeScore`
  - Returns `StatusResult` (all data, no formatting)

New file: `pkg/townctl/status_format.go`
- `FormatStatusText(r *StatusResult, opts FormatOpts) string`
- `FormatStatusJSON(r *StatusResult) ([]byte, error)`

New case in `cmd/town-ctl/main.go`:
```go
case "status":
    if err := statusCmd(args[1:]); err != nil { ... }
```

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Fleet fully converged (score == 1.0 for all rigs) |
| 1 | Error (Dolt connection failed, query error) |
| 2 | Fleet not fully converged (score < 1.0 for any rig) |

Exit code 2 allows CI pipelines to gate on convergence:
```bash
town-ctl status --output=json && echo "fleet ready"
```

## Non-Goals

- Does not write anything to Dolt
- Does not start or stop any agent
- Does not replace `town-ctl apply` for desired-state changes
- Does not require the observer binary to be running
