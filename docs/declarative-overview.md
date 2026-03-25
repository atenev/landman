# Declarative Gas Town: Conceptual Overview

> **See also**: [town.toml Reference](town-toml-reference.md) for a complete key-by-key
> schema reference.

---

## The GitOps mental model

The central idea behind dgt is simple: **a single file in git describes everything about
your Gas Town instance**.

```toml
# town.toml
version = "1"

[town]
name = "my-town"
home = "${GT_HOME}"

[defaults]
mayor_model   = "claude-opus-4-6"
polecat_model = "claude-sonnet-4-6"
max_polecats  = 20

[[rig]]
name   = "backend"
repo   = "${PROJECTS_DIR}/backend"
branch = "main"
```

Commit this file to git. From that moment, the desired state of your Gas Town instance is
version-controlled, reviewable in PRs, diffable across environments, and fully reproducible
from scratch. No runbooks, no tribal knowledge, no CLI history to reconstruct.

This is GitOps applied to AI agent orchestration: the repository *is* the source of truth.

---

## How desired state flows to Gas Town

`town.toml` is never parsed directly by Gas Town. Instead, an **actuator** translates it
into rows in Dolt — Gas Town's git-for-SQL control plane. Gas Town's Mayor and Deacon watch
those rows and converge to match.

```
town.toml  →  actuator  →  desired_topology (Dolt)  →  Gas Town converges
```

Two actuators exist, both writing the **identical Dolt schema**:

| Actuator | When to use |
|---|---|
| `town-ctl apply town.toml` | Local laptop, baremetal server, CI pipeline |
| `gastown-operator` (K8s) | Kubernetes cluster with ArgoCD / Flux |

This split means you can start on a laptop and graduate to a 600-agent Kubernetes cluster
without changing your `town.toml` or migrating any Dolt tables. The actuator is swappable;
the desired state is not.

---

## Why secrets never reach Dolt

`town.toml` is safe to commit to git because **secrets never leave the actuator process**.

```toml
[secrets]
anthropic_api_key = "${ANTHROPIC_API_KEY}"
github_token      = "${GITHUB_TOKEN}"
```

`town-ctl` resolves every `${VAR}` expression against the environment at apply time and
injects the values directly into agent processes. They are never written to Dolt, never
logged, and never appear in git history. If any referenced variable is unset, `town-ctl`
exits immediately with a clear error before opening a Dolt connection.

An optional `file = "~/.gt/secrets.toml"` (gitignored) allows a local secrets file as an
alternative to environment variables for development environments where env vars are
inconvenient.

---

## The GUPP principle: writes before actions

Gas Town uses the **GUPP invariant** as its crash-safety protocol. In plain language:
*record what you intend to do before you do it*.

Every dgt component follows GUPP:

- `town-ctl` writes the entire desired topology to Dolt in a single atomic transaction
  *before* launching any processes or signalling any agents.
- The Surveyor records each reconcile attempt as a Dolt branch (`reconcile/<uuid>`) before
  executing a single operation. A crash mid-reconcile leaves the branch intact for audit;
  the next Surveyor boot re-reads current state and continues.
- Deacon writes cost-ledger entries before a Polecat exits, not after.

The benefit: crash recovery requires no event replay. Any component can restart, read the
current Dolt state, and resume deterministically. This makes the whole system resilient to
the partial failures that AI workloads produce at scale.

---

## Composing topologies with `includes`

Large installations often manage dozens of rigs across teams. The `includes` field lets you
split `town.toml` into per-rig fragments without losing the single-source-of-truth guarantee.

```toml
# town.toml — just the global knobs
version = "1"
[defaults]
max_polecats = 20
includes = ["./rigs/*.toml"]
```

```toml
# rigs/backend.toml
[[rig]]
name = "backend"
repo = "${PROJECTS_DIR}/backend"
```

`town-ctl` resolves the glob, loads every matching file, and merges the `[[rig]]` arrays
before the Dolt write. Duplicate rig names across files are a hard error — no silent
last-wins behaviour.

---

## Environment overlays

When dev and prod share the same topology but differ in scale and budget, use `--overlay`:

```bash
town-ctl apply town.toml                           # development
town-ctl apply town.toml --overlay town.prod.toml  # production
```

The overlay file sets only the values that differ. Scalar fields in the overlay win over
the base. Your production diff is minimal and reviewable:

```toml
# town.prod.toml
[defaults]
max_polecats     = 100
mayor_model      = "claude-opus-4-6"
daily_budget_usd = 500.0

[town.agents]
surveyor = true
```

---

## Extending Gas Town with custom roles

Gas Town has seven built-in agent roles. `dgt` lets you add your own without modifying
the `gt` binary.

```toml
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
```

Rigs opt in explicitly:

```toml
[[rig]]
name = "backend"
  [rig.agents]
  roles = ["reviewer"]
```

Custom roles participate in Gas Town through the same surfaces every built-in role uses:
Dolt for shared state, Beads for work items. The `gt` binary treats them as external Claude
Code processes — which is exactly what they are.

Every custom role must declare a supervision relationship (`parent` must be a built-in
role). This prevents orphaned agents and keeps the accountability chain intact.

---

## Cost controls

Gas Town burns ~$100/hr at peak. `dgt` adds opt-in cost governance at two scopes:

```toml
[defaults.cost]
daily_budget_usd = 200.0   # town-wide safety net
warn_at_pct      = 80      # warn at 80%, drain at 100%

[[rig]]
name = "backend"
  [rig.cost]
  daily_budget_usd = 50.0  # per-rig override
  warn_at_pct      = 75
```

**Unrestricted is the explicit default.** A rig with no `[rig.cost]` block and no
`[defaults.cost]` block has no cost row in Dolt — Deacon skips cost patrol entirely.
Existing Gas Town setups that adopt `dgt` are unaffected unless they add a cost block.

Subscription users (Claude Max) who cannot use USD budgets can use `daily_budget_messages`
or `daily_budget_tokens` instead. The budget type is per-policy, not per-installation.

---

## Migration path for existing Gas Town users

`dgt` is additive. The `gt` binary is never modified. Your existing Gas Town instance keeps
running while you adopt `dgt` incrementally:

1. **Start with `town-ctl` for one rig.** Write a minimal `town.toml` describing a single
   rig. Run `town-ctl apply --dry-run` to preview the Dolt writes. Apply when ready.

2. **Add more rigs.** Expand `town.toml` rig by rig. Each apply is idempotent — running
   twice with the same manifest is a no-op on the second run.

3. **Enable the Surveyor.** Once all rigs are in `town.toml`, set
   `[town.agents] surveyor = true`. From this point, topology changes are driven entirely
   by git commits.

4. **Adopt cost controls (optional).** Add `[defaults.cost]` or `[rig.cost]` to any rig
   that needs spend governance.

5. **Graduate to Kubernetes (optional).** Replace `town-ctl` with the `gastown-operator`.
   No Dolt schema migration required.

At every step, you remain in control. There is no flag day, no forced migration, and no
`gt` binary to upgrade.
