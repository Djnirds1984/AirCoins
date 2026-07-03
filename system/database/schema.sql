-- ============================================
-- AirCoins PisoWiFi - PostgreSQL Database Schema
-- ============================================
-- Database: aircoins
-- Run as: sudo -u postgres psql -f schema.sql
-- ============================================

-- Create database
CREATE DATABASE aircoins;

-- Connect to aircoins database
\c aircoins;

-- ============================================
-- ADMIN USERS
-- ============================================
CREATE TABLE admin_users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    last_login TIMESTAMP
);

-- Insert default admin user (password: admin123)
-- Hash generated with bcrypt
INSERT INTO admin_users (username, password_hash) 
VALUES ('admin', '$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy');

-- ============================================
-- SYSTEM SETTINGS
-- ============================================
CREATE TABLE system_settings (
    id SERIAL PRIMARY KEY,
    key VARCHAR(100) UNIQUE NOT NULL,
    value TEXT NOT NULL,
    description TEXT,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Insert default settings
INSERT INTO system_settings (key, value, description) VALUES
    ('ssid', 'AirCoins_Free', 'WiFi network name'),
    ('bandwidth', '50', 'Bandwidth limit in Mbps'),
    ('max_session', '120', 'Maximum session time in minutes'),
    ('portal_ip', '192.168.42.1', 'Portal IP address'),
    ('wifi_interface', 'wlan0', 'WiFi interface name'),
    ('eth_interface', 'eth0', 'Ethernet interface name');

-- ============================================
-- GPIO CONFIGURATION
-- ============================================
CREATE TABLE gpio_config (
    id SERIAL PRIMARY KEY,
    pin INTEGER NOT NULL DEFAULT 7,
    coin_value INTEGER NOT NULL DEFAULT 1,
    pulse_mode VARCHAR(20) NOT NULL DEFAULT 'falling',
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Insert default GPIO config
INSERT INTO gpio_config (pin, coin_value, pulse_mode) VALUES (7, 1, 'falling');

-- ============================================
-- PRICING TABLE
-- ============================================
CREATE TABLE pricing (
    id SERIAL PRIMARY KEY,
    coin_value INTEGER NOT NULL,
    minutes INTEGER NOT NULL,
    active BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- Insert default pricing
INSERT INTO pricing (coin_value, minutes) VALUES
    (1, 5),
    (5, 30),
    (10, 60);

-- ============================================
-- COIN EVENTS (raw coin detections)
-- ============================================
CREATE TABLE coin_events (
    id SERIAL PRIMARY KEY,
    coin_value INTEGER NOT NULL,
    detected_at TIMESTAMP DEFAULT NOW(),
    processed BOOLEAN DEFAULT false,
    session_id INTEGER REFERENCES sessions(id)
);

-- ============================================
-- SESSIONS
-- ============================================
CREATE TABLE sessions (
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

-- Index for faster queries
CREATE INDEX idx_sessions_status ON sessions(status);
CREATE INDEX idx_sessions_client_ip ON sessions(client_ip);
CREATE INDEX idx_sessions_started_at ON sessions(started_at);

-- ============================================
-- SYSTEM LOGS
-- ============================================
CREATE TABLE system_logs (
    id SERIAL PRIMARY KEY,
    level VARCHAR(10) NOT NULL DEFAULT 'INFO', -- INFO, WARN, ERROR, DEBUG
    component VARCHAR(50), -- gpio, session, admin, system
    message TEXT NOT NULL,
    metadata JSONB,
    created_at TIMESTAMP DEFAULT NOW()
);

-- Index for log queries
CREATE INDEX idx_logs_level ON system_logs(level);
CREATE INDEX idx_logs_created_at ON system_logs(created_at);
CREATE INDEX idx_logs_component ON system_logs(component);

-- ============================================
-- DAILY STATS (aggregated)
-- ============================================
CREATE TABLE daily_stats (
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
CREATE INDEX idx_daily_stats_date ON daily_stats(date);

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
CREATE TRIGGER trigger_update_daily_stats
AFTER INSERT OR UPDATE ON sessions
FOR EACH ROW
EXECUTE FUNCTION update_daily_stats();

-- ============================================
-- VIEWS
-- ============================================

-- View for today's stats
CREATE VIEW today_stats AS
SELECT 
    COALESCE(SUM(total_earnings), 0) as earnings,
    COALESCE(SUM(total_coins), 0) as coins,
    COALESCE(SUM(total_sessions), 0) as sessions
FROM daily_stats
WHERE date = CURRENT_DATE;

-- View for active sessions
CREATE VIEW active_sessions AS
SELECT * FROM sessions
WHERE status = 'active' AND remaining_seconds > 0;

-- ============================================
-- PERMISSIONS
-- ============================================
-- Grant permissions to www-data for CGI scripts
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA public TO www-data;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO www-data;

-- ============================================
-- COMPLETION
-- ============================================
\echo 'Database schema created successfully!'
\echo 'Default admin credentials: admin / admin123'
