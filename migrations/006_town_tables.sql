-- Migration: 006_town_tables
-- Issues:    dgt-199
-- Purpose:   Create desired_town and actual_town Dolt tables.
--
--   desired_town  — written by the K8s GasTownReconciler (ADR-0003).
--                   One row per GasTown CR: records the resolved defaults for
--                   mayor_model, polecat_model, and max_polecats, plus the
--                   town's home directory.
--
--   actual_town   — written by the Surveyor after each reconcile cycle.
--                   One row per GasTown instance: records the timestamp of
--                   the last successful reconcile (used by the operator to
--                   surface status.lastReconcileAt on the GasTown CR).

-- ============================================================================
-- desired_town
--   One row per GasTown CR.
--   Written by GasTownReconciler.syncToDolt in a versions-first transaction
--   (ADR-0003: desired_topology_versions upserted first).
-- ============================================================================
CREATE TABLE IF NOT EXISTS desired_town (
    name          VARCHAR(128)  NOT NULL,
    home          TEXT          NOT NULL,
    mayor_model   VARCHAR(256)  NOT NULL DEFAULT 'claude-opus-4-6',
    polecat_model VARCHAR(256)  NOT NULL DEFAULT 'claude-sonnet-4-6',
    max_polecats  INT           NOT NULL DEFAULT 20,
    updated_at    TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (name)
);

-- ============================================================================
-- actual_town
--   One row per GasTown instance, written by the Surveyor after each
--   reconcile cycle completes.  The operator's status-sync goroutine reads
--   last_reconcile_at to patch GasTown.status.lastReconcileAt.
-- ============================================================================
CREATE TABLE IF NOT EXISTS actual_town (
    name               VARCHAR(128)  NOT NULL,
    last_reconcile_at  TIMESTAMP     NULL,
    PRIMARY KEY (name)
);

-- ============================================================================
-- Seed desired_topology_versions (idempotent).
-- ============================================================================
INSERT INTO desired_topology_versions (table_name, schema_version, written_by)
VALUES ('desired_town', 1, 'migration/006')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;

-- ============================================================================
-- Seed actual_topology_versions (idempotent).
-- ============================================================================
INSERT INTO actual_topology_versions (table_name, schema_version, written_by)
VALUES ('actual_town', 1, 'migration/006')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run these statements to roll back:
--
--   DELETE FROM actual_topology_versions  WHERE table_name = 'actual_town';
--   DELETE FROM desired_topology_versions WHERE table_name = 'desired_town';
--   DROP TABLE IF EXISTS actual_town;
--   DROP TABLE IF EXISTS desired_town;
