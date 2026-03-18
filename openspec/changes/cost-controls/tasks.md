## 1. Dolt Schema — desired_cost_policy and cost_ledger

- [ ] 1.1 Design and document `desired_cost_policy` table schema and `cost_ledger_24h` view [dgt-msk]
- [ ] 1.2 Design and document `cost_ledger` table schema with `(rig_name, recorded_at)` index [dgt-5bc]
- [ ] 1.3 Write Dolt SQL migration files for both tables and the view [dgt-i71]
- [ ] 1.4 Add `desired_topology_versions` upsert for `desired_cost_policy` to migration [dgt-i71]

## 2. Manifest Format — [rig.cost] and [defaults.cost]

- [ ] 2.1 Define `RigCost` and `DefaultsCost` Go structs with `go-validator` mutual-exclusion tags [dgt-doh]
- [ ] 2.2 Extend `Defaults` and `Rig` structs to include `Cost *RigCost` sub-struct [dgt-doh]
- [ ] 2.3 Generate/update JSON Schema to include `[rig.cost]` and `[defaults.cost]` blocks [dgt-doh]
- [ ] 2.4 Validate: exactly one of `daily_budget_usd`, `daily_budget_messages`, `daily_budget_tokens`
  may be set; zero budget fields in a present cost block is a hard error [dgt-doh]
- [ ] 2.5 Validate: `warn_at_pct` in range [1, 99]; `daily_budget_usd` > 0 [dgt-doh]

## 3. town-ctl — Cost Policy Parsing and Dolt Write

- [ ] 3.1 Implement cost policy resolution: rig → defaults → unrestricted inheritance chain [dgt-2xf]
- [ ] 3.2 Implement `desired_cost_policy` row upsert in existing atomic Dolt transaction [dgt-2xf]
- [ ] 3.3 Implement `desired_cost_policy` row DELETE for rigs removed from manifest [dgt-2xf]
- [ ] 3.4 Implement `desired_topology_versions` upsert for `desired_cost_policy` (first in transaction,
  ADR-0003) [dgt-2xf]
- [ ] 3.5 Extend `--dry-run` output to show `desired_cost_policy` add/update/remove plan [dgt-2xf]

## 4. Polecat CLAUDE.md — cost_ledger Write Before Exit

- [ ] 4.1 Add `cost_ledger` INSERT instruction to Polecat CLAUDE.md (GUPP write-before-exit) [dgt-8wm]
- [ ] 4.2 Add static model pricing table (opus-4-6, sonnet-4-6, haiku-4-5) with USD per 1M tokens [dgt-8wm]
- [ ] 4.3 Add `GT_BILLING_TYPE=subscription` rule: write `cost_usd = NULL` [dgt-8wm]
- [ ] 4.4 Add unknown model handling: write `cost_usd = NULL`, preserve model name [dgt-8wm]

## 5. Deacon — Cost Patrol

- [ ] 5.1 Implement cost patrol query against `desired_cost_policy` LEFT JOIN `cost_ledger_24h` [dgt-jw4]
- [ ] 5.2 Implement `pct_used` computation per `budget_type` (usd/messages/tokens) [dgt-jw4]
- [ ] 5.3 Implement hard cap enforcement: file drain Bead (priority=0, tag=cost-cap) [dgt-jw4]
- [ ] 5.4 Implement soft warning: file Mayor Bead (priority=1, tag=cost-warning) [dgt-jw4]
- [ ] 5.5 Implement duplicate Bead prevention: check for existing open cost-cap/cost-warning Bead
  per rig before filing [dgt-jw4]
- [ ] 5.6 Make patrol interval configurable via `[town.cost] patrol_interval_seconds` (default 300) [dgt-jw4]

## 6. Tests

- [ ] 6.1 Unit: mutual exclusion validation — two budget fields set, zero budget fields set [dgt-2au]
- [ ] 6.2 Unit: `warn_at_pct` range validation (0, 100, 101 all rejected; 1, 80, 99 accepted) [dgt-2au]
- [ ] 6.3 Unit: inheritance chain — rig overrides defaults, both absent = unrestricted [dgt-2au]
- [ ] 6.4 Integration: `town-ctl apply` with `[rig.cost]` — verify `desired_cost_policy` row [dgt-2au]
- [ ] 6.5 Integration: `town-ctl apply` with no cost blocks — verify no `desired_cost_policy` rows [dgt-2au]
- [ ] 6.6 Integration: `town-ctl apply` removes rig — verify `desired_cost_policy` row deleted [dgt-2au]
- [ ] 6.7 Integration: Deacon patrol at 85% warn — verify Mayor Bead filed [dgt-2au]
- [ ] 6.8 Integration: Deacon patrol at 103% hard cap — verify drain Bead filed [dgt-2au]
- [ ] 6.9 Integration: Deacon patrol with unrestricted rig (no policy row) — verify no Beads filed [dgt-2au]
- [ ] 6.10 Integration: Deacon patrol duplicate prevention — second cycle at cap, verify no new Bead [dgt-2au]

## 7. Documentation

- [ ] 7.1 Update annotated `town.toml` examples with `[rig.cost]` blocks (API billing, subscription,
  inherited, unrestricted) [dgt-cub]
- [ ] 7.2 Add `[rig.cost]` extension slot entry to reserved-slots documentation [dgt-su1]
