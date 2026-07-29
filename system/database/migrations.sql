-- ============================================
-- AirCoins PisoNet - Idempotent migrations
-- ============================================
-- Applied on EVERY install/update run (install.sh), after schema.sql.
-- Safe to run repeatedly on an already-provisioned device.
--
-- Run manually as (non-interactive, via postgres superuser peer auth):
--   sudo -u postgres psql -v ON_ERROR_STOP=1 -d aircoins -c 'SET ROLE aircoins;' -f migrations.sql
-- ============================================

-- Pin the schema explicitly: current_schema() checks below must resolve to
-- 'public' whether this file runs as the aircoins role or as postgres
-- (whose default "$user" schema does not exist).
SET search_path = public;

-- ============================================
-- 001 - PRICING: drop rate-per-minute
-- ============================================
-- The pricing model is: 1 peso coin = 1 pulse, and a pricing row maps
-- the inserted peso amount to minutes. Any rate-per-minute column from
-- older installs is unused and is removed here.
ALTER TABLE IF EXISTS pricing DROP COLUMN IF EXISTS rate_per_minute;
ALTER TABLE IF EXISTS pricing DROP COLUMN IF EXISTS rate_per_min;

-- ============================================
-- 002 - PRICING: purge seeded default tiers (once)
-- ============================================
-- Older schema.sql seeded (1,5), (5,30), (10,60). The pricing table must
-- start blank so the operator enters every tier manually.
--
-- Guarded by a marker in system_settings so this runs exactly once: if the
-- operator deliberately re-creates a tier that happens to match a former
-- default, a later migration run will NOT delete it again.
DO $$
BEGIN
    -- Nothing to do on a database that has no pricing/settings tables yet
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'pricing'
    ) OR NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'system_settings'
    ) THEN
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM system_settings WHERE key = 'pricing_defaults_purged'
    ) THEN
        DELETE FROM pricing
        WHERE (coin_value, minutes) IN ((1, 5), (5, 30), (10, 60));

        INSERT INTO system_settings (key, value, description)
        VALUES ('pricing_defaults_purged', 'true',
                'Seeded default pricing tiers removed (pricing starts blank)')
        ON CONFLICT (key) DO NOTHING;

        RAISE NOTICE 'Migration 002: seeded default pricing tiers purged';
    END IF;
END
$$;

-- ============================================
-- COMPLETION
-- ============================================
\echo 'Migrations applied.'
