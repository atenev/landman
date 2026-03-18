-- Migration: 001_desired_topology
-- Issue:     dgt-9ft
-- Purpose:   Create the desired_topology Dolt tables that town-ctl writes into
--            as the coupling point between the actuator and Gas Town.
--
-- Versioning strategy: ADR-0003 — no per-row schema_version columns.
-- desired_topology_versions is the single versioning authority for all tables.
-- town-ctl writes to it first in every apply transaction.
-- The Surveyor reads it first in every reconcile loop (pre-flight check).

-- ---------------------------------------------------------------------------
-- desired_topology_versions
--   One row per topology table. Written by town-ctl first in every transaction.
--   Read by the Surveyor before touching any topology data.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_topology_versions (
    table_name     VARCHAR(128)  NOT NULL,
    schema_version INT           NOT NULL,
    written_by     VARCHAR(128),             -- e.g. "town-ctl/0.1.0"
    written_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (table_name)
);

-- ---------------------------------------------------------------------------
-- desired_rigs
--   One row per rig declared in town.toml.
--   Desired state is always the full resolved manifest: rows absent from the
--   current apply are deleted (no append-only mode).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_rigs (
    name       VARCHAR(128)  NOT NULL,
    repo       TEXT          NOT NULL,
    branch     VARCHAR(256)  NOT NULL,
    enabled    BOOLEAN       NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (name)
);

-- ---------------------------------------------------------------------------
-- desired_agent_config
--   One row per (rig, role) pair.
--   Roles: mayor, witness, refinery, deacon, polecat.
--   A disabled role has no row — absence means disabled.
--   max_count is null for non-polecat roles (only polecats have a pool size).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_agent_config (
    rig_name       VARCHAR(128)  NOT NULL,
    role           VARCHAR(64)   NOT NULL,       -- mayor|witness|refinery|deacon|polecat
    enabled        BOOLEAN       NOT NULL DEFAULT TRUE,
    model          VARCHAR(256),                 -- NULL means inherit from [defaults]
    max_count      INT,                          -- only meaningful for polecat role
    claude_md_path TEXT,                         -- resolved path; NULL means built-in default
    PRIMARY KEY (rig_name, role),
    CONSTRAINT fk_agent_config_rig
        FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
            ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT chk_role_enum
        CHECK (role IN ('mayor', 'witness', 'refinery', 'deacon', 'polecat'))
);

-- ---------------------------------------------------------------------------
-- desired_formulas
--   One row per formula declared under [[rig.formula]].
--   schedule is a cron expression (standard 5-field: min hour dom mon dow).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_formulas (
    rig_name VARCHAR(128) NOT NULL,
    name     VARCHAR(128) NOT NULL,
    schedule VARCHAR(128) NOT NULL,             -- cron expression, e.g. "0 2 * * *"
    PRIMARY KEY (rig_name, name),
    CONSTRAINT fk_formulas_rig
        FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
            ON DELETE CASCADE ON UPDATE CASCADE
);

-- ---------------------------------------------------------------------------
-- Seed desired_topology_versions at schema version 1.
-- This is idempotent: an UPSERT so re-running the migration is safe.
-- ---------------------------------------------------------------------------
INSERT INTO desired_topology_versions (table_name, schema_version, written_by)
VALUES
    ('desired_rigs',          1, 'migration/001'),
    ('desired_agent_config',  1, 'migration/001'),
    ('desired_formulas',      1, 'migration/001')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;
