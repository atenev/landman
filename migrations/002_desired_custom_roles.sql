-- Migration: 002_desired_custom_roles
-- Issues:    dgt-uxa, dgt-lai, dgt-40u
-- Purpose:   Create desired_custom_roles and desired_rig_custom_roles tables
--            for the declarative custom agent role definitions (ADR-0004).
--
-- Versioning strategy: ADR-0003 — no per-row schema_version columns.
-- desired_topology_versions is the single versioning authority.

-- ============================================================================
-- UP migration
-- ============================================================================

-- ---------------------------------------------------------------------------
-- desired_custom_roles
--   One row per [[role]] entry in town.toml.
--   scope=rig roles require a corresponding desired_rig_custom_roles row to
--   activate on a rig.  scope=town roles are active globally.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_custom_roles (
    name             VARCHAR(128)                                          NOT NULL,
    description      TEXT,
    scope            ENUM('town', 'rig')                                   NOT NULL,
    lifespan         ENUM('ephemeral', 'persistent')                       NOT NULL DEFAULT 'ephemeral',
    trigger_type     ENUM('bead_assigned', 'schedule', 'event', 'manual') NOT NULL,
    trigger_schedule VARCHAR(64),       -- required when trigger_type='schedule'
    trigger_event    VARCHAR(128),      -- required when trigger_type='event'
    claude_md_path   VARCHAR(512)                                          NOT NULL,
    model            VARCHAR(128),      -- NULL means inherit from rig defaults
    parent_role      VARCHAR(128)                                          NOT NULL,
    reports_to       VARCHAR(128),      -- NULL means same as parent_role
    max_instances    INT                NOT NULL DEFAULT 1,
    PRIMARY KEY (name),
    CONSTRAINT chk_custom_role_name_not_builtin
        CHECK (name NOT IN ('mayor', 'polecat', 'witness', 'refinery', 'deacon', 'dog', 'crew')),
    CONSTRAINT chk_trigger
        CHECK (
            (trigger_type = 'schedule' AND trigger_schedule IS NOT NULL)
            OR (trigger_type = 'event'    AND trigger_event    IS NOT NULL)
            OR (trigger_type IN ('bead_assigned', 'manual'))
        )
);

-- ---------------------------------------------------------------------------
-- desired_rig_custom_roles
--   Junction table: which rigs opt in to which custom roles.
--   Only consulted for scope='rig' roles; scope='town' roles need no row here.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS desired_rig_custom_roles (
    rig_name  VARCHAR(128) NOT NULL,
    role_name VARCHAR(128) NOT NULL,
    enabled   BOOLEAN      NOT NULL DEFAULT TRUE,
    PRIMARY KEY (rig_name, role_name),
    CONSTRAINT fk_rig_custom_roles_rig
        FOREIGN KEY (rig_name)  REFERENCES desired_rigs(name)
            ON DELETE CASCADE ON UPDATE CASCADE,
    CONSTRAINT fk_rig_custom_roles_role
        FOREIGN KEY (role_name) REFERENCES desired_custom_roles(name)
            ON DELETE CASCADE ON UPDATE CASCADE
);

-- ---------------------------------------------------------------------------
-- Register schema version 1 for both new tables.
-- Idempotent: safe to re-run.
-- ---------------------------------------------------------------------------
INSERT INTO desired_topology_versions (table_name, schema_version, written_by)
VALUES
    ('desired_custom_roles',     1, 'migration/002'),
    ('desired_rig_custom_roles', 1, 'migration/002')
ON DUPLICATE KEY UPDATE
    schema_version = VALUES(schema_version),
    written_by     = VALUES(written_by),
    written_at     = CURRENT_TIMESTAMP;

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run these statements in reverse dependency order to roll back this migration.
--
--   DELETE FROM desired_topology_versions
--     WHERE table_name IN ('desired_custom_roles', 'desired_rig_custom_roles');
--   DROP TABLE IF EXISTS desired_rig_custom_roles;
--   DROP TABLE IF EXISTS desired_custom_roles;
