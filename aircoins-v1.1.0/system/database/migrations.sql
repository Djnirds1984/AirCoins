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
-- 002 - SYSTEM_SETTINGS: ensure UNIQUE (key)
-- ============================================
-- The API (admin.go) and migration 005 below use
-- INSERT ... ON CONFLICT (key), which requires a unique constraint or
-- unique index on system_settings.key. Databases created from an old
-- schema.sql may lack it, making those statements fail with:
--   pq: there is no unique or exclusion constraint matching the ON CONFLICT specification
-- MUST run before migration 005 (which relies on ON CONFLICT (key)).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'system_settings'
    ) THEN
        RETURN;
    END IF;

    -- Already enforced by any non-partial unique index on exactly (key)
    -- (covers both UNIQUE constraints and plain unique indexes — either
    -- satisfies ON CONFLICT (key) arbiter inference)
    IF EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_class t ON t.oid = i.indrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE n.nspname = current_schema()
          AND t.relname = 'system_settings'
          AND i.indisunique
          AND i.indpred IS NULL
          AND i.indnkeyatts = 1
          AND (SELECT a.attname FROM pg_attribute a
               WHERE a.attrelid = t.oid AND a.attnum = i.indkey[0]) = 'key'
    ) THEN
        RETURN;
    END IF;

    -- Remove duplicate keys first (keep the most recently updated row),
    -- otherwise ALTER TABLE below fails
    DELETE FROM system_settings s
    USING system_settings k
    WHERE s.key = k.key
      AND s.id <> k.id
      AND (COALESCE(s.updated_at, 'epoch'::timestamp), s.id)
        < (COALESCE(k.updated_at, 'epoch'::timestamp), k.id);

    ALTER TABLE system_settings ADD CONSTRAINT system_settings_key_key UNIQUE (key);
    RAISE NOTICE 'Migration 002: UNIQUE constraint added on system_settings(key)';
END
$$;

-- ============================================
-- 003 - PRICING: ensure UNIQUE (coin_value)
-- ============================================
-- The API (pricing.go, "Add Pricing Tier") uses
-- INSERT ... ON CONFLICT (coin_value). Databases created from an old
-- schema.sql have a pricing table without the unique constraint, so the
-- insert fails. Current schema.sql declares coin_value UNIQUE inline —
-- this backfills existing databases.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'pricing'
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_class t ON t.oid = i.indrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE n.nspname = current_schema()
          AND t.relname = 'pricing'
          AND i.indisunique
          AND i.indpred IS NULL
          AND i.indnkeyatts = 1
          AND (SELECT a.attname FROM pg_attribute a
               WHERE a.attrelid = t.oid AND a.attnum = i.indkey[0]) = 'coin_value'
    ) THEN
        RETURN;
    END IF;

    -- Remove duplicate tiers first (keep the most recently updated row),
    -- otherwise ALTER TABLE below fails
    DELETE FROM pricing p
    USING pricing q
    WHERE p.coin_value = q.coin_value
      AND p.id <> q.id
      AND (COALESCE(p.updated_at, p.created_at, 'epoch'::timestamp), p.id)
        < (COALESCE(q.updated_at, q.created_at, 'epoch'::timestamp), q.id);

    ALTER TABLE pricing ADD CONSTRAINT pricing_coin_value_key UNIQUE (coin_value);
    RAISE NOTICE 'Migration 003: UNIQUE constraint added on pricing(coin_value)';
END
$$;

-- ============================================
-- 004 - DAILY_STATS: ensure UNIQUE (date)
-- ============================================
-- The update_daily_stats() trigger function in schema.sql uses
-- INSERT ... ON CONFLICT (date). If the function is ever (re)applied on a
-- database whose daily_stats table predates the UNIQUE(date) column
-- constraint, every session INSERT/UPDATE would fail. Backfill it here.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'daily_stats'
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_index i
        JOIN pg_class t ON t.oid = i.indrelid
        JOIN pg_namespace n ON n.oid = t.relnamespace
        WHERE n.nspname = current_schema()
          AND t.relname = 'daily_stats'
          AND i.indisunique
          AND i.indpred IS NULL
          AND i.indnkeyatts = 1
          AND (SELECT a.attname FROM pg_attribute a
               WHERE a.attrelid = t.oid AND a.attnum = i.indkey[0]) = 'date'
    ) THEN
        RETURN;
    END IF;

    -- Remove duplicate dates first (keep the most recently updated row),
    -- otherwise ALTER TABLE below fails
    DELETE FROM daily_stats d
    USING daily_stats e
    WHERE d.date = e.date
      AND d.id <> e.id
      AND (COALESCE(d.updated_at, d.created_at, 'epoch'::timestamp), d.id)
        < (COALESCE(e.updated_at, e.created_at, 'epoch'::timestamp), e.id);

    ALTER TABLE daily_stats ADD CONSTRAINT daily_stats_date_key UNIQUE (date);
    RAISE NOTICE 'Migration 004: UNIQUE constraint added on daily_stats(date)';
END
$$;

-- ============================================
-- 005 - PRICING: purge seeded default tiers (once)
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

        RAISE NOTICE 'Migration 005: seeded default pricing tiers purged';
    END IF;
END
$$;

-- ============================================
-- 006 - SESSIONS: wall-clock expiry (expires_at)
-- ============================================
-- The API enforces session expiry against sessions.expires_at (a session
-- is alive while expires_at > NOW()); remaining_seconds is only kept as a
-- display snapshot. Databases created from an old schema.sql lack the
-- column, so add it and backfill still-active sessions from their
-- remaining_seconds so a paying client is not cut off by the update.
ALTER TABLE IF EXISTS sessions ADD COLUMN IF NOT EXISTS expires_at TIMESTAMP;

UPDATE sessions
SET expires_at = NOW() + (remaining_seconds * INTERVAL '1 second')
WHERE status = 'active'
  AND expires_at IS NULL
  AND COALESCE(remaining_seconds, 0) > 0;

CREATE INDEX IF NOT EXISTS idx_sessions_client_mac ON sessions(client_mac);
CREATE INDEX IF NOT EXISTS idx_sessions_status_expires_at ON sessions(status, expires_at);

-- Recreate the view wall-clock based (schema.sql keeps a legacy-safe
-- definition because it runs before this file on old databases).
CREATE OR REPLACE VIEW active_sessions AS
SELECT * FROM sessions
WHERE status = 'active' AND expires_at > NOW();

-- ============================================
-- 007 - VLAN/PORTAL SPLIT: portal_servers
-- ============================================
-- VLAN creation now provisions ONLY the 802.1Q interface; the hotspot
-- stack (portal IP, DHCP range, DNS hijack, captive rules) moved to
-- the new portal_servers table (one row per interface). Every old
-- vlan_config row that carries an IP is auto-migrated into a
-- portal_servers row so working portals (e.g. end0.22 at 10.0.22.1/24,
-- DHCP .100-.200) keep running with zero manual reconfiguration, then
-- the legacy vlan_config columns are dropped.
--
-- DHCP range derivation matches the old Go default (calculateDHCPRange):
-- start = start_ip if set, else network+100; end = start+100.
CREATE TABLE IF NOT EXISTS portal_servers (
    id SERIAL PRIMARY KEY,
    interface VARCHAR(32) UNIQUE NOT NULL,
    portal_ip_cidr VARCHAR(18) NOT NULL,
    dhcp_start VARCHAR(15) NOT NULL DEFAULT '',
    dhcp_end VARCHAR(15) NOT NULL DEFAULT '',
    dhcp_lease VARCHAR(20) NOT NULL DEFAULT '12h',
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

DO $$
DECLARE
    r RECORD;
    v_start inet;
    has_ip_address BOOLEAN;
    has_start_ip   BOOLEAN;
    has_is_portal  BOOLEAN;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'vlan_config'
    ) THEN
        RETURN;
    END IF;

    -- Probe EACH legacy column individually: an interrupted or partial
    -- earlier run may have left any combination (e.g. ip_address still
    -- present but start_ip already dropped). The conversion query below
    -- is built from these flags so it never references a missing column.
    SELECT
        EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = current_schema()
                  AND table_name = 'vlan_config' AND column_name = 'ip_address'),
        EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = current_schema()
                  AND table_name = 'vlan_config' AND column_name = 'start_ip'),
        EXISTS (SELECT 1 FROM information_schema.columns
                WHERE table_schema = current_schema()
                  AND table_name = 'vlan_config' AND column_name = 'is_portal')
    INTO has_ip_address, has_start_ip, has_is_portal;

    -- Fresh installs and fully-migrated databases: nothing legacy left,
    -- exit without touching the table at all.
    IF NOT (has_ip_address OR has_start_ip OR has_is_portal) THEN
        RETURN;
    END IF;

    -- Convert only while legacy ip_address data is still readable. When
    -- start_ip is already gone (partial state) substitute '' so the
    -- remaining rows still convert — DHCP start then derives from the
    -- network address, exactly the old Go default.
    IF has_ip_address THEN
        FOR r IN EXECUTE format(
            'SELECT interface, vlan_id, ip_address, %s AS start_ip
             FROM vlan_config WHERE COALESCE(ip_address, '''') <> ''''',
            CASE WHEN has_start_ip
                 THEN 'COALESCE(start_ip, '''')'
                 ELSE '''''' END)
        LOOP
            BEGIN
                IF r.start_ip <> '' THEN
                    v_start := r.start_ip::inet;
                ELSE
                    v_start := network(r.ip_address::inet)::inet + 100;
                END IF;

                INSERT INTO portal_servers
                    (interface, portal_ip_cidr, dhcp_start, dhcp_end, dhcp_lease, enabled)
                VALUES (
                    r.interface || '.' || r.vlan_id,
                    r.ip_address,
                    host(v_start),
                    host(v_start + 100),
                    '12h',
                    true
                )
                ON CONFLICT (interface) DO NOTHING;
            EXCEPTION WHEN OTHERS THEN
                RAISE NOTICE 'Migration 007: skipped %.% (%): %',
                    r.interface, r.vlan_id, r.ip_address, SQLERRM;
            END;
        END LOOP;
    END IF;

    -- Clear whatever legacy columns remain — safe under every partial
    -- combination.
    ALTER TABLE vlan_config DROP COLUMN IF EXISTS ip_address;
    ALTER TABLE vlan_config DROP COLUMN IF EXISTS start_ip;
    ALTER TABLE vlan_config DROP COLUMN IF EXISTS is_portal;

    RAISE NOTICE 'Migration 007: vlan_config split — portal data moved to portal_servers';
END
$$;

-- ============================================
-- 008 - SESSIONS: one row per device (client_mac)
-- ============================================
-- The Sessions admin page showed the same device several times (active,
-- expired, expired, ...) because every new payment after expiry inserted
-- a fresh row. The API now UPSERTs on client_mac (reusing the expired
-- row), which requires a unique partial index. Dedupe first: per MAC keep
-- the active row if any, otherwise the most recent one, and re-point
-- coin_events at the survivor before deleting the duplicates.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'sessions'
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = current_schema()
          AND c.relname = 'sessions_client_mac_uniq'
          AND c.relkind = 'i'
    ) THEN
        RETURN;
    END IF;

    -- Re-point coin_events at the surviving row (FK would otherwise
    -- block the DELETE below)
    UPDATE coin_events ce
    SET session_id = k.keep_id
    FROM (
        SELECT DISTINCT ON (client_mac) id AS keep_id, client_mac
        FROM sessions
        WHERE client_mac IS NOT NULL AND client_mac <> '' AND client_mac <> '-'
        ORDER BY client_mac, (status = 'active') DESC,
                 started_at DESC NULLS LAST, id DESC
    ) k
    JOIN sessions dup ON dup.client_mac = k.client_mac AND dup.id <> k.keep_id
    WHERE ce.session_id = dup.id;

    DELETE FROM sessions s
    USING (
        SELECT DISTINCT ON (client_mac) id AS keep_id, client_mac
        FROM sessions
        WHERE client_mac IS NOT NULL AND client_mac <> '' AND client_mac <> '-'
        ORDER BY client_mac, (status = 'active') DESC,
                 started_at DESC NULLS LAST, id DESC
    ) k
    WHERE s.client_mac = k.client_mac
      AND s.id <> k.keep_id;

    CREATE UNIQUE INDEX sessions_client_mac_uniq
    ON sessions(client_mac)
    WHERE client_mac IS NOT NULL AND client_mac <> '' AND client_mac <> '-';

    RAISE NOTICE 'Migration 008: sessions deduped, unique index on client_mac added';
END
$$;

-- ============================================
-- 009 - PORTAL APPEARANCE: seed default config
-- ============================================
-- The captive portal look (theme id, colors, background image) is ONE
-- JSON document in system_settings under key 'portal_appearance',
-- served by GET /api/portal/appearance and edited from the admin
-- "Portal" page. Seed the default dark preset ONLY when the key does
-- not exist yet, so an operator's saved theme is never overwritten by
-- a reinstall.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'system_settings'
    ) THEN
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM system_settings WHERE key = 'portal_appearance'
    ) THEN
        INSERT INTO system_settings (key, value, description)
        VALUES (
            'portal_appearance',
            '{"theme":"dark","colors":{"primary":"#1a1a2e","accent":"#ffd700","background":"#16213e","card":"#1f2b47","text":"#eaeaea","button":"#ffa500","button_text":"#1a1a2e"},"background_image":""}',
            'Captive portal appearance (theme, colors, background image) as a JSON document'
        )
        ON CONFLICT (key) DO NOTHING;

        RAISE NOTICE 'Migration 009: default portal_appearance seeded';
    END IF;
END
$$;

-- ============================================
-- 010 - TAP ANTI-ABUSE + SESSION PAUSE
-- ============================================
-- Two portal features: per-device ban for tap-spamming (INSERT COIN),
-- and real pause/resume that also closes the client's internet while
-- the session is frozen. Adds pause columns to sessions, two ephemeral
-- tables (client_bans + tap_activity), and seeds the rules as JSON
-- in system_settings.

ALTER TABLE IF EXISTS sessions ADD COLUMN IF NOT EXISTS paused_at TIMESTAMPTZ;
ALTER TABLE IF EXISTS sessions ADD COLUMN IF NOT EXISTS remaining_seconds_at_pause INT;
ALTER TABLE IF EXISTS sessions ADD COLUMN IF NOT EXISTS pause_count INT DEFAULT 0;

CREATE TABLE IF NOT EXISTS client_bans (
    id SERIAL PRIMARY KEY,
    client_mac VARCHAR(17) UNIQUE NOT NULL,
    banned_until TIMESTAMPTZ NOT NULL,
    reason VARCHAR(32),
    attempts_in_window INT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_client_bans_until ON client_bans(banned_until);

CREATE TABLE IF NOT EXISTS tap_activity (
    client_mac VARCHAR(17) PRIMARY KEY,
    counter INT NOT NULL DEFAULT 0,
    window_started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Seed portal tap rules + pause rules IF NOT EXISTS. Guarded by a
-- system_settings table existence check so very old databases (pre-
-- migration 002) don't fail — they will be seeded on a later run.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'system_settings'
    ) THEN
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM system_settings WHERE key = 'portal_tap_rules') THEN
        INSERT INTO system_settings (key, value, description)
        VALUES ('portal_tap_rules',
                '{"max_taps":5,"window_seconds":60,"ban_seconds":300}',
                'INSERT COIN anti-abuse limits (per MAC, per window)');
    END IF;

    IF NOT EXISTS (SELECT 1 FROM system_settings WHERE key = 'portal_pause_rules') THEN
        INSERT INTO system_settings (key, value, description)
        VALUES ('portal_pause_rules',
                '{"pause_limit":0}',
                'Session pause rules (pause_limit=0 means unlimited)');
    END IF;
END
$$;

-- ============================================
-- 011 - SESSIONS: per-session speed override
-- ============================================
-- Adds shaped_mbps (INT) to sessions so an admin can override the
-- per-device HTB class rate for one session. NULL = use the portal's
-- global per_device_bw_mbps from portal_qdisc_rules; >0 = override.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables
        WHERE table_schema = current_schema() AND table_name = 'sessions'
    ) THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'sessions' AND column_name = 'shaped_mbps'
    ) THEN
        RETURN;
    END IF;

    ALTER TABLE sessions ADD COLUMN shaped_mbps INT;
    RAISE NOTICE 'Migration 011: shaped_mbps column added to sessions';
END
$$;

-- ============================================
-- 012 - SESSIONS: per-device session token for MAC-randomization roaming
-- ============================================
-- A device roaming between SSIDs may change its MAC (phone MAC
-- randomization). The session token (8 hex chars, generated client-side)
-- identifies the device across MAC changes so the remaining time can be
-- migrated to the new MAC transparently.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS session_token VARCHAR(8);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_session_token
  ON sessions(session_token)
  WHERE session_token IS NOT NULL AND session_token <> '';

-- ============================================
-- COMPLETION
-- ============================================
\echo 'Migrations applied.'
