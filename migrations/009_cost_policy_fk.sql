-- Migration: 009_cost_policy_fk
-- Issues:    dgt-yc6
-- Purpose:   Add FOREIGN KEY from desired_cost_policy(rig_name) to
--            desired_rigs(name) ON DELETE CASCADE ON UPDATE CASCADE.
--
--            Without this constraint, deleting a rig from desired_rigs leaves
--            an orphaned desired_cost_policy row. Deacon then attempts to
--            enforce a cost policy for a rig that no longer exists, producing
--            spurious errors.
--
-- Depends on: migration 001 (desired_rigs), migration 004 (desired_cost_policy).
-- Apply migrations in order: 001, 002, 003, 004, 005, 006, 007, 008, then 009.

-- ============================================================================
-- UP migration
-- ============================================================================

-- Before adding the FK, purge any pre-existing orphaned rows so the ALTER
-- cannot fail with a foreign key violation. In a freshly-provisioned cluster
-- this is a no-op.
DELETE FROM desired_cost_policy
WHERE rig_name NOT IN (SELECT name FROM desired_rigs);

ALTER TABLE desired_cost_policy
    ADD CONSTRAINT fk_cost_policy_rig
        FOREIGN KEY (rig_name) REFERENCES desired_rigs(name)
            ON DELETE CASCADE
            ON UPDATE CASCADE;

-- ============================================================================
-- DOWN migration
-- ============================================================================
-- Run this statement to roll back this migration:
--
--   ALTER TABLE desired_cost_policy DROP FOREIGN KEY fk_cost_policy_rig;
