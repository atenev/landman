-- Migration: 004_desired_cost_policy
-- Issues:    dgt-msk, dgt-i71
-- Purpose:   Create the desired_cost_policy Dolt table that town-ctl writes and
--            Deacon reads for cost enforcement. Also defines the cost_ledger_24h
--            rolling view that Deacon's cost patrol query uses (ADR-0006).
--
-- Versioning strategy: ADR-0003 — desired_cost_policy IS a desired_topology table.
--   desired_topology_versions is seeded below at schema_version = 1.
--   No per-row schema_version column: ADR-0003 explicitly prohibits this.
--
-- Depends on: migration 003 (cost_ledger table must exist before CREATE VIEW).
-- Apply migrations in order: 001, 002, 003, then 004.

-- ============================================================================
-- UP migration
-- ============================================================================

-- ---------------------------------------------------------------------------
-- desired_cost_policy
--   One row per rig that has an active cost policy.
--   Absence of a row means the rig is unrestricted — Deacon skips cost patrol.
--   Written by town-ctl at apply time. Deleted when a rig is removed from town.toml.
--
--   budget_type selects which signal Deacon enforces:
--     'usd'      → daily_budget is in USD (API billing users)
--     'messages' → daily_budget is a message count (subscription users)
--     'tokens'   → daily_budget is a token count (subscription users)
--
--   warn_at_pct: Deacon files a warning Bead when pct_used >= warn_at_pct.
--               At 100% Deacon files a hard-cap drain Bead. Range: [1, 99].
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_cost_policy (
    rig_name     VARCHAR(128)                       NOT NULL,
    budget_type  ENUM('usd', 'messages', 'tokens')  NOT NULL,
    daily_budget DECIMAL(16,4)                      NOT NULL,
    warn_at_pct  TINYINT                            NOT NULL DEFAULT 80,
    PRIMARY KEY (rig_name),
    CONSTRAINT chk_warn_at_pct_range
        CHECK (warn_at_pct BETWEEN 1 AND 99),
    CONSTRAINT chk_daily_budget_positive
        CHECK (daily_budget > 0)
);

-- ---------------------------------------------------------------------------
-- cost_ledger_24h
--   Rolling 24-hour spend aggregates, grouped by rig.
--   Deacon's cost patrol LEFT JOINs desired_cost_policy against this view.
--   A rig absent from cost_ledger (no spend in the last 24h) has no row here;
--   the LEFT JOIN produces NULLs that Deacon treats as 0% used.
--
--   Column names (spec: cost-controls/specs/cost-ledger):
--     spend_usd      — SUM(cost_usd), NULL for subscription-only rigs
--     spend_messages — SUM(message_count)
--     spend_tokens   — SUM(input_tokens + output_tokens)
--
--   Requires: cost_ledger table created in migration 003.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW cost_ledger_24h AS
SELECT
    rig_name,
    SUM(cost_usd)                     AS spend_usd,
    SUM(message_count)                AS spend_messages,
    SUM(input_tokens + output_tokens) AS spend_tokens
FROM cost_ledger
WHERE recorded_at >= NOW() - INTERVAL 1 DAY
GROUP BY rig_name;

-- ---------------------------------------------------------------------------
-- Seed desired_topology_versions at schema version 1 (ADR-0003 contract).
-- town-ctl upserts this row first in every apply transaction that touches
-- desired_cost_policy. This seed covers the initial migration itself.
-- Idempotent: safe to re-run.
-- ---------------------------------------------------------------------------
INSERT INTO desired_topology_versions (table_name, schema_version, written_by)
VALUES ('desired_cost_policy', 1, 'migration/004')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run these statements in reverse dependency order to roll back this migration.
-- Apply 005 DOWN first if actual_topology tables exist.
--
--   DELETE FROM desired_topology_versions WHERE table_name = 'desired_cost_policy';
--   DROP VIEW  IF EXISTS cost_ledger_24h;
--   DROP TABLE IF EXISTS desired_cost_policy;
