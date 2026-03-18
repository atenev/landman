## ADDED Requirements

### Requirement: `cost_ledger` table exists with correct schema
The `cost_ledger` Dolt table SHALL have columns: `id BIGINT AUTO_INCREMENT PRIMARY KEY`,
`rig_name VARCHAR(128) NOT NULL`, `polecat_id VARCHAR(64) NOT NULL`, `model VARCHAR(128)
NOT NULL`, `input_tokens INT NOT NULL`, `output_tokens INT NOT NULL`, `cost_usd
DECIMAL(12,6) NULL`, `message_count INT NOT NULL DEFAULT 1`, `recorded_at TIMESTAMP NOT
NULL DEFAULT CURRENT_TIMESTAMP`. An index on `(rig_name, recorded_at)` SHALL exist to
support the 24h rolling window query.

#### Scenario: Table created by migration
- **WHEN** the DDL migration is applied
- **THEN** `cost_ledger` exists with the correct schema and index

### Requirement: `cost_ledger_24h` view exists and returns correct aggregates
The `cost_ledger_24h` view SHALL aggregate `cost_ledger` rows from the last 24 hours,
grouped by `rig_name`, returning `spend_usd` (SUM of cost_usd), `spend_messages` (SUM
of message_count), `spend_tokens` (SUM of input_tokens + output_tokens).

#### Scenario: View returns 24h aggregates
- **WHEN** `cost_ledger` contains rows for rig "backend" with `recorded_at` values both
  within and outside the last 24 hours
- **THEN** `cost_ledger_24h` returns only the sum of rows within the last 24 hours

#### Scenario: Rig with no rows returns no result
- **WHEN** `cost_ledger` has no rows for rig "docs" in the last 24 hours
- **THEN** `cost_ledger_24h` returns no row for "docs" (LEFT JOIN in Deacon query handles NULL)

### Requirement: Polecat writes one row to cost_ledger before exit
Each Polecat process SHALL write exactly one row to `cost_ledger` as the last action
before exiting. This write MUST occur before the process exits — it is a GUPP invariant.

#### Scenario: Polecat completes task and exits
- **WHEN** a Polecat finishes its assigned task
- **THEN** a row is present in `cost_ledger` with the correct `rig_name`, `polecat_id`,
  `model`, `input_tokens`, `output_tokens`, and `message_count`

### Requirement: API billing Polecats write non-null cost_usd
A Polecat running in API billing mode (i.e., `GT_BILLING_TYPE` env var is NOT set to
`subscription`) SHALL compute `cost_usd` from the static pricing table in its CLAUDE.md
and write a non-null value.

#### Scenario: API billing Polecat using claude-sonnet-4-6
- **WHEN** `GT_BILLING_TYPE` is unset and the Polecat used `claude-sonnet-4-6` with
  100,000 input tokens and 10,000 output tokens
- **THEN** `cost_usd` = (100000/1000000 × 3.00) + (10000/1000000 × 15.00) = 0.45

### Requirement: Subscription Polecats write NULL cost_usd
A Polecat running with `GT_BILLING_TYPE=subscription` SHALL write `cost_usd = NULL`.

#### Scenario: Subscription Polecat
- **WHEN** `GT_BILLING_TYPE=subscription` is set in the Polecat's environment
- **THEN** the `cost_ledger` row has `cost_usd = NULL`

### Requirement: message_count is always 1 per Polecat exit row
Each Polecat is one task → one LLM conversation → one Polecat process. `message_count`
in the exit row SHALL always be 1. It counts the number of Polecat exits recorded, not
the number of individual LLM API calls within the task.

#### Scenario: Polecat exit row message_count
- **WHEN** any Polecat writes its exit row
- **THEN** `message_count = 1`

### Requirement: Static pricing table covers current Claude models
The Polecat CLAUDE.md SHALL include a pricing table covering at minimum:
`claude-opus-4-6`, `claude-sonnet-4-6`, `claude-haiku-4-5-20251001`. Prices are in
USD per 1M tokens (input/output separately).

Current prices (as of 2026-03):
- `claude-opus-4-6`: $15.00 / $75.00 per 1M input/output
- `claude-sonnet-4-6`: $3.00 / $15.00 per 1M input/output
- `claude-haiku-4-5-20251001`: $0.25 / $1.25 per 1M input/output

#### Scenario: Unknown model
- **WHEN** a Polecat ran using a model not in the pricing table
- **THEN** `cost_usd` is written as NULL with a note in a `cost_note` field (or the
  row is written with NULL and the unknown model name is preserved in `model` column
  for operator investigation)
