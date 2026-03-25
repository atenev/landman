# dgt — Declarative Gas Town [Landman]

> **Declarative topology layer for [Gas Town](https://github.com/steveyegge/gas-town)**
> — define your entire multi-agent AI orchestrator as code.

> [!NOTE]
> This project is developed **entirely by AI agents**.
> Every design decision, spec, and line of code is authored by Claude Code instances
> coordinated through the primitives this project itself documents.

---

## What is this?

[Gas Town](https://github.com/steveyegge/gas-town) (Steve Yegge, Jan 2026) is a
multi-agent AI coding orchestrator written in Go (~189k lines). The core idea: run 20–30
Claude Code instances in parallel on isolated git worktrees, coordinated by seven
specialised agent roles — Mayor, Polecats, Witness, Refinery, Deacon, Dogs, and Crew.

Today, setting up a Gas Town instance requires a sequence of imperative CLI commands:

```bash
gt install
gt rig add --repo=/path/to/repo --branch=main
gt config set mayor.model claude-opus-4-6
gt config set polecats.max 20
# … repeated per rig, per environment
```

There is no single artifact that describes a complete Gas Town topology. **dgt** fixes that.

It adds a **declarative control plane** on top of Gas Town — without modifying the `gt`
binary. The result is GitOps-native, auditable, crash-resilient topology management for
single-host development environments and 600-agent Kubernetes clusters alike.

---

## Architecture overview

```
                  ┌──────────────────────────────────────────┐
                  │           Desired State (git)             │
                  │            town.toml / CRDs               │
                  └──────────┬──────────────┬────────────────┘
                             │              │
                    ┌────────▼──────┐ ┌─────▼──────────────┐
                    │   town-ctl    │ │   gastown-operator  │
                    │  (CLI apply)  │ │   (K8s controller)  │
                    └────────┬──────┘ └─────┬───────────────┘
                             │              │
                    ┌────────▼──────────────▼────────────────┐
                    │         Dolt  (git-for-SQL)             │
                    │   desired_topology tables               │
                    │   desired_topology_versions             │
                    │   desired_cost_policy                   │
                    │   cost_ledger                           │
                    └─────────────────┬──────────────────────┘
                                      │  change feed
                    ┌─────────────────▼──────────────────────┐
                    │           The Surveyor                  │
                    │  (Claude Code agent — topology          │
                    │   reconciler, no gt modification)       │
                    └──────┬──────────────────────┬──────────┘
                           │ bd create             │ verify loop
                    ┌──────▼──────┐        ┌──────▼───────────┐
                    │    Dogs     │        │  actual_topology  │
                    │ (executors) │        │   (Dolt tables)   │
                    └─────────────┘        └──────────────────┘
```

**Key principle**: the actuator (`town-ctl` or the K8s operator) writes desired state to
Dolt. Gas Town converges via the Surveyor, an external Claude Code agent. The `gt` binary
is never modified.

---

## Components

### `town.toml` — declarative manifest

A single TOML file describes the full desired state of a Gas Town instance:

```toml
version = "1"

[town]
name      = "my-town"
home      = "${GT_HOME}"
dolt_port = 3306

  [town.agents]
  surveyor = true   # enable topology reconciler

[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20

  [defaults.cost]
  daily_budget_usd = 200.0   # town-wide safety net (API billing)
  warn_at_pct      = 80

[secrets]
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"

includes = ["./rigs/*.toml"]

[[role]]
name  = "reviewer"
scope = "rig"
  [role.identity]
  claude_md = "${GT_HOME}/roles/reviewer/CLAUDE.md"
  [role.trigger]
  type = "bead_assigned"
  [role.supervision]
  parent     = "witness"
  reports_to = "mayor"
  [role.resources]
  max_instances = 3

[[rig]]
name    = "backend"
repo    = "${PROJECTS_DIR}/backend"
branch  = "main"
enabled = true

  [rig.agents]
  mayor        = true
  witness      = true
  refinery     = true
  deacon       = true
  max_polecats = 30
  polecat_model = "claude-haiku-4-5-20251001"
  roles         = ["reviewer"]

  [rig.cost]
  daily_budget_usd = 50.0
  warn_at_pct      = 75

  [[rig.formula]]
  name     = "nightly-tests"
  schedule = "0 2 * * *"
```

### `town-ctl` — local actuator

Standalone binary (no Gas Town internals). Responsibilities:

- Parse and validate `town.toml` (JSON Schema + `go-validator`)
- Resolve `includes` and environment overlays
- Resolve secrets via env-var interpolation — **never written to Dolt**
- Diff resolved desired state against current Dolt `desired_topology` tables
- Write a single atomic Dolt transaction (supports `--dry-run`)
- Launch the Surveyor process if `[town.agents] surveyor = true`

```bash
town-ctl apply town.toml
town-ctl apply town.toml --dry-run
town-ctl apply town.toml --overlay=town.prod.toml
```

### The Surveyor — AI topology reconciler

A long-lived Claude Code process identified by its `CLAUDE.md`. It is **invisible to `gt`**
and participates through Dolt and Beads — the same surfaces every other Gas Town agent uses.

- Subscribes to `desired_topology` changes via Dolt change feed
- Uses LLM reasoning (not a hardcoded diff) to plan safe, minimal operations
- Delegates execution to Dogs via `bd create` operation Beads
- Runs a two-layer verify loop (Dolt state + process health) before confirming convergence
- Every reconcile attempt is a Dolt branch `reconcile/<uuid>` — success merges, failure retains the abandoned branch for audit
- GUPP-compliant: on crash, re-reads current state and reconciles from scratch

### `gastown-operator` — Kubernetes actuator

Optional. Deploys as a standard Kubernetes operator. Four CRDs:

| CRD | Scope | Purpose |
|-----|-------|---------|
| `GasTown` | Cluster-scoped | Town metadata, Surveyor Deployment |
| `Rig` | Namespaced | Per-rig desired topology |
| `AgentRole` | Namespaced | Custom agent roles |
| `DoltInstance` | Namespaced | Dolt StatefulSet + PVC + Service |

The operator writes the **identical** `desired_topology` schema as `town-ctl`. Switching
from local CLI to K8s operator requires no Dolt schema migration.

```yaml
apiVersion: gastown.io/v1alpha1
kind: GasTown
metadata:
  name: my-town
spec:
  version: "1"
  doltRef:
    name: my-town-dolt
    namespace: gastown-system
  defaults:
    mayorModel: claude-opus-4-6
    polecatModel: claude-sonnet-4-6
    maxPolecats: 20
  agents:
    surveyor: true
    surveyorClaudeMdRef:
      name: surveyor-claude-md
  secretsRef:
    name: gastown-secrets
```

### Cost controls

Gas Town burns ~$100/hr at peak. `dgt` adds opt-in governance:

- **API billing users**: `daily_budget_usd` — Deacon drains rigs at 100%, warns at `warn_at_pct`
- **Subscription users**: `daily_budget_messages` or `daily_budget_tokens`
- Per-rig override of `[defaults.cost]`; no block = unrestricted (no regression)
- `cost_ledger` Dolt table: per-Polecat spend audit, written before Polecat exit (GUPP)

### Custom agent roles

Extend Gas Town's seven built-in roles without modifying `gt`:

- Declare globally as `[[role]]` in `town.toml`; rigs opt in via `roles = [...]`
- CLAUDE.md as a file reference (path interpolation supported)
- Mandatory supervision relationship — every custom role must have a `parent` built-in
- Four trigger types: `bead_assigned`, `schedule`, `event`, `manual`
- Workflow insertion via Bead dependency chains, no binary changes

---

## Installation

### Prerequisites

- **Go 1.25+** — `go version` should report `go1.25` or later
- **Dolt** — the Git-for-SQL database that serves as the control plane
- **Git** — for cloning this repo and working with `town.toml`
- **(Kubernetes deployments only)** `kubectl` + a running cluster

### Build from source

```bash
git clone https://github.com/tenev/dgt
cd dgt

# Install town-ctl into $GOPATH/bin
go install ./cmd/town-ctl

# Or build locally
go build -o ./bin/town-ctl ./cmd/town-ctl
```

### Binary install via Nix (recommended)

```bash
# Installs the latest town-ctl into your Nix profile
nix profile install github:tenev/dgt

# Drop into a dev shell with town-ctl, dolt, and kubectl pre-loaded
nix develop github:tenev/dgt
```

### NixOS with a flake

Add `dgt` as a flake input and enable the module:

```nix
# flake.nix
inputs.dgt.url = "github:tenev/dgt";
inputs.dgt.inputs.nixpkgs-unstable.follows = "nixpkgs";
```

```nix
# configuration.nix
{ ... }:
{
  imports = [ inputs.dgt.nixosModules.default ];

  services.dgt = {
    enable      = true;
    configFile  = /etc/gt/town.toml;
    autoApply.enable          = true;
    autoApply.environmentFile = "/run/secrets/dgt-env";
  };
}
```

See [docs/nix-module.md](docs/nix-module.md) for the full option reference and
secret-management guidance.

### NixOS without a flake (fetchTarball)

```nix
# configuration.nix
let
  dgt = builtins.fetchTarball {
    url    = "https://github.com/tenev/dgt/archive/<git-rev>.tar.gz";
    sha256 = "<sha256>";
  };
in
{
  imports = [ "${dgt}/nix/modules/town-ctl.nix" ];
  nixpkgs.overlays = [ (import "${dgt}/nix/overlays/default.nix") ];

  services.dgt = {
    enable      = true;
    configFile  = /etc/gt/town.toml;
    autoApply.enable          = true;
    autoApply.environmentFile = "/run/secrets/dgt-env";
  };
}
```

Recalculate the hash after any rev bump:

```bash
nix-prefetch-url --unpack https://github.com/tenev/dgt/archive/<new-rev>.tar.gz
```

### First-run steps

1. **Install Dolt** and start it as a local server:

   ```bash
   # macOS / Linux via shell script
   curl -L https://github.com/dolthub/dolt/releases/latest/download/install.sh | bash

   # Or via Nix
   nix profile install nixpkgs#dolt

   dolt sql-server --host=127.0.0.1 --port=3306 &
   ```

2. **Write a minimal `town.toml`** (see `docs/examples/town.minimal.toml` for a
   full annotated example):

   ```toml
   version = "1"

   [town]
   name      = "my-town"
   home      = "${GT_HOME}"
   dolt_port = 3306

   [secrets]
   anthropic_api_key = "${ANTHROPIC_API_KEY}"
   ```

3. **Apply the topology**:

   ```bash
   export GT_HOME=~/.gt
   export ANTHROPIC_API_KEY=sk-ant-…

   town-ctl apply town.toml
   # Add --dry-run to preview changes without writing to Dolt
   ```

---

## Documentation

| Document | Description |
|----------|-------------|
| [docs/nix-module.md](docs/nix-module.md) | NixOS module option reference, systemd units, secret management, and full configuration examples |
| [docs/townctl/design.md](docs/townctl/design.md) | `town-ctl` design and architecture |

---

## Design decisions (ADRs)

| ADR | Title | Status |
|-----|-------|--------|
| [ADR-0001](docs/adr/0001-town-toml-declarative-topology.md) | Declarative Town Topology — Format and Actuator Design | Proposed |
| [ADR-0002](docs/adr/0002-surveyor-topology-reconciler.md) | The Surveyor — Topology Reconciler Design | Proposed |
| [ADR-0003](docs/adr/0003-desired-topology-schema-versioning.md) | `desired_topology` Schema Versioning Strategy | Proposed |
| [ADR-0004](docs/adr/0004-declarative-custom-agent-roles.md) | Declarative Custom Agent Role Definitions | Proposed |
| [ADR-0005](docs/adr/0005-k8s-operator-crd-design.md) | Kubernetes Operator and CRD Design | Proposed |
| [ADR-0006](docs/adr/0006-declarative-cost-controls.md) | Declarative Cost Controls | Proposed |

---

## Key invariants

**Secrets never reach Dolt.** `town-ctl` resolves secrets at apply time and injects them
as env vars. The `town.toml` is safe to commit to git.

**No `gt` binary modification.** Every dgt component is external to `gt`. It participates
through Dolt (shared SQL state) and Beads (work items) — the same surfaces `gt` agents use.

**`desired_topology_versions` is always written first.** Every actuator (`town-ctl`,
`gastown-operator`, future Flux plugin) upserts this table as the first SQL statement in
every apply transaction. The Surveyor reads it as its first operation in every reconcile
loop — a hard version compatibility check before touching any topology data.

**GUPP compliance.** Every component that writes to Dolt or Beads does so before taking
the action the write describes. Crash recovery re-reads current state; no event replay required.

**Unrestricted is the explicit default.** Absent `[rig.cost]` and absent `[defaults.cost]`
means no `desired_cost_policy` row — Deacon skips cost patrol entirely. Existing Gas Town
setups are unaffected by adding the `dgt` cost schema.

---

## Development workflow

This project uses [Beads](https://github.com/steveyegge/gas-town) for issue tracking and
is developed autonomously by AI agents. See [AGENTS.md](AGENTS.md) for the worker protocol.

```bash
bd ready                  # see available work
bd show <id>              # issue details
bd update <id> --claim    # atomic claim
bd close <id>             # mark complete
```

Work items are tracked in Dolt (git-for-SQL) alongside the topology specs. Every `bd`
operation is a Dolt commit — the full project history is queryable SQL.

---

## Deployment contexts

| Context | Actuator | Scale |
|---------|----------|-------|
| Laptop / single VM | `town-ctl apply` | 1–30 agents |
| Baremetal server | `town-ctl apply` + systemd | 30–160 agents |
| Kubernetes cluster | `gastown-operator` + ArgoCD/Flux | 160–600 agents |

All contexts write the same `desired_topology` Dolt schema. The Surveyor and Gas Town are
indifferent to which actuator wrote the tables.

---

## Status

All ADRs are in **Proposed** state. Implementation has not started — the ADRs define the
complete design contract for agent-driven implementation. See `bd ready` for open work items.
