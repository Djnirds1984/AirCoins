-- ============================================
-- AirCoins PisoNet - PostgreSQL Database Schema
-- ============================================
-- Database: aircoins
-- Run as:  psql -U aircoins -d aircoins -f schema.sql
-- (install.sh creates the database and user before running this file)
-- ============================================

-- ============================================
-- ADMIN USERS
-- ============================================
CREATE TABLE IF NOT EXISTS admin_users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    last_login TIMESTAMP
);

-- Insert default admin user (password: admin123)
-- Hash generated with bcrypt
INSERT INTO admin_users (username, password_hash)
VALUES ('admin', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy')
ON CONFLICT (username) DO NOTHING;

-- ============================================
-- SYSTEM SETTINGS
-- ============================================
CREATE TABLE IF NOT EXISTS system_settings (
    id SERIAL PRIMARY KEY,
    key VARCHAR(100) UNIQUE NOT NULL,
    value TEXT NOT NULL,
    description TEXT,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Insert default settings
INSERT INTO system_settings (key, value, description) VALUES
    ('bandwidth', '50', 'Bandwidth limit in Mbps'),
    ('max_session', '120', 'Maximum session time in minutes'),
    ('eth_interface', 'eth0', 'Ethernet interface name'),
    ('portal_ip', '', 'Portal IP address for admin access')
ON CONFLICT (key) DO NOTHING;

-- ============================================
-- GPIO CONFIGURATION
-- ============================================
CREATE TABLE IF NOT EXISTS gpio_config (
    id SERIAL PRIMARY KEY,
    pin INTEGER NOT NULL DEFAULT 7,
    coin_value INTEGER NOT NULL DEFAULT 1,
    pulse_mode VARCHAR(20) NOT NULL DEFAULT 'falling',
    board_model VARCHAR(50) DEFAULT 'auto',
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Insert default GPIO config (only if table is empty)
INSERT INTO gpio_config (pin, coin_value, pulse_mode)
SELECT 7, 1, 'falling'
WHERE NOT EXISTS (SELECT 1 FROM gpio_config LIMIT 1);

-- ============================================
-- PRICING TABLE
-- ============================================
CREATE TABLE IF NOT EXISTS pricing (
    id SERIAL PRIMARY KEY,
    coin_value INTEGER NOT NULL UNIQUE,
    minutes INTEGER NOT NULL,
    active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_pricing_coin_value ON pricing(coin_value);

-- Insert default pricing (only if table is empty)
INSERT INTO pricing (coin_value, minutes)
SELECT * FROM (VALUES (1, 5), (5, 30), (10, 60)) AS v(coin_value, minutes)
WHERE NOT EXISTS (SELECT 1 FROM pricing LIMIT 1);

-- ============================================
-- SESSIONS (must come before coin_events — FK dependency)
-- ============================================
CREATE TABLE IF NOT EXISTS sessions (
    id SERIAL PRIMARY KEY,
    client_ip VARCHAR(45),
    client_mac VARCHAR(17),
    coins_inserted INTEGER DEFAULT 0,
    total_seconds INTEGER DEFAULT 0,
    remaining_seconds INTEGER DEFAULT 0,
    status VARCHAR(20) DEFAULT 'pending', -- pending, active, expired, cancelled
    started_at TIMESTAMP DEFAULT NOW(),
    activated_at TIMESTAMP,
    expired_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW()
);

-- Indexes for faster queries
CREATE INDEX IF NOT EXISTS idx_sessions_status ON sessions(status);
CREATE INDEX IF NOT EXISTS idx_sessions_client_ip ON sessions(client_ip);
CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at);

-- Compound indexes for pagination and filter queries
CREATE INDEX IF NOT EXISTS idx_sessions_started_at_desc ON sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_status_started_at ON sessions(status, started_at DESC);

-- ============================================
-- COIN EVENTS (raw coin detections)
-- ============================================
CREATE TABLE IF NOT EXISTS coin_events (
    id SERIAL PRIMARY KEY,
    coin_value INTEGER NOT NULL,
    detected_at TIMESTAMP DEFAULT NOW(),
    processed BOOLEAN DEFAULT false,
    session_id INTEGER REFERENCES sessions(id)
);

-- ============================================
-- SYSTEM LOGS
-- ============================================
CREATE TABLE IF NOT EXISTS system_logs (
    id SERIAL PRIMARY KEY,
    level VARCHAR(10) NOT NULL DEFAULT 'INFO', -- INFO, WARN, ERROR, DEBUG
    component VARCHAR(50), -- gpio, session, admin, system
    message TEXT NOT NULL,
    metadata JSONB,
    created_at TIMESTAMP DEFAULT NOW()
);

-- Indexes for log queries
CREATE INDEX IF NOT EXISTS idx_logs_level ON system_logs(level);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON system_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_component ON system_logs(component);

-- ============================================
-- DAILY STATS (aggregated)
-- ============================================
CREATE TABLE IF NOT EXISTS daily_stats (
    id SERIAL PRIMARY KEY,
    date DATE UNIQUE NOT NULL,
    total_earnings INTEGER DEFAULT 0,
    total_coins INTEGER DEFAULT 0,
    total_sessions INTEGER DEFAULT 0,
    active_users INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Index for date queries
CREATE INDEX IF NOT EXISTS idx_daily_stats_date ON daily_stats(date);

-- ============================================
-- VLAN CONFIGURATION
-- ============================================
CREATE TABLE IF NOT EXISTS vlan_config (
    id SERIAL PRIMARY KEY,
    interface VARCHAR(20) NOT NULL,
    vlan_id INTEGER NOT NULL,
    ip_address VARCHAR(18) NOT NULL,
    description VARCHAR(100) DEFAULT '',
    is_portal BOOLEAN DEFAULT false,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(interface, vlan_id)
);

-- Default portal VLAN setting
INSERT INTO system_settings (key, value, description) VALUES
    ('portal_vlan', '', 'Primary VLAN ID for portal access')
ON CONFLICT (key) DO NOTHING;

-- ============================================
-- FUNCTIONS
-- ============================================

-- Function to update daily stats
CREATE OR REPLACE FUNCTION update_daily_stats()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO daily_stats (date, total_earnings, total_coins, total_sessions)
    VALUES (
        CURRENT_DATE,
        COALESCE(NEW.coins_inserted, 0),
        CASE WHEN NEW.coins_inserted > 0 THEN 1 ELSE 0 END,
        CASE WHEN NEW.status = 'active' THEN 1 ELSE 0 END
    )
    ON CONFLICT (date) DO UPDATE SET
        total_earnings = daily_stats.total_earnings + COALESCE(NEW.coins_inserted, 0),
        total_coins = daily_stats.total_coins + (CASE WHEN NEW.coins_inserted > 0 THEN 1 ELSE 0 END),
        total_sessions = daily_stats.total_sessions + (CASE WHEN NEW.status = 'active' THEN 1 ELSE 0 END),
        updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Trigger to auto-update daily stats when session changes
-- Drop first since CREATE TRIGGER has no IF NOT EXISTS in older PG versions
DROP TRIGGER IF EXISTS trigger_update_daily_stats ON sessions;
CREATE TRIGGER trigger_update_daily_stats
AFTER INSERT OR UPDATE ON sessions
FOR EACH ROW
EXECUTE FUNCTION update_daily_stats();

-- ============================================
-- VIEWS
-- ============================================

-- View for today's stats
CREATE OR REPLACE VIEW today_stats AS
SELECT 
    COALESCE(SUM(total_earnings), 0) as earnings,
    COALESCE(SUM(total_coins), 0) as coins,
    COALESCE(SUM(total_sessions), 0) as sessions
FROM daily_stats
WHERE date = CURRENT_DATE;

-- View for active sessions
CREATE OR REPLACE VIEW active_sessions AS
SELECT * FROM sessions
WHERE status = 'active' AND remaining_seconds > 0;

-- ============================================
-- PERMISSIONS
-- ============================================
-- Grant permissions to www-data for CGI scripts
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA public TO "www-data";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO "www-data";

-- ============================================
-- COMPLETION
-- ============================================
\echo 'Database schema created successfully!'
\echo 'Default admin credentials: admin / admin123'
