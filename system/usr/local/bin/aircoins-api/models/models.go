package models

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

var DB *sql.DB

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
}

func InitDB(config DBConfig) error {
	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		config.Host, config.Port, config.User, config.Password, config.DBName)

	var err error
	DB, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	err = DB.Ping()
	if err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Set connection pool settings
	DB.SetMaxOpenConns(25)
	DB.SetMaxIdleConns(5)
	DB.SetConnMaxLifetime(5 * time.Minute)

	return nil
}

// ============================================
// MODELS
// ============================================

type AdminUser struct {
	ID           int        `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLogin    *time.Time `json:"last_login,omitempty"`
}

type SystemSetting struct {
	ID          int       `json:"id"`
	Key         string    `json:"key"`
	Value       string    `json:"value"`
	Description string    `json:"description,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type GPIOConfig struct {
	ID         int       `json:"id"`
	Pin        int       `json:"pin"`
	CoinValue  int       `json:"coin_value"`
	PulseMode  string    `json:"pulse_mode"`
	BoardModel string    `json:"board_model"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Pricing struct {
	ID        int       `json:"id"`
	CoinValue int       `json:"coin_value"`
	Minutes   int       `json:"minutes"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CoinEvent struct {
	ID         int       `json:"id"`
	CoinValue  int       `json:"coin_value"`
	DetectedAt time.Time `json:"detected_at"`
	Processed  bool      `json:"processed"`
	SessionID  *int      `json:"session_id,omitempty"`
}

type Session struct {
	ID                      int        `json:"id"`
	ClientIP                string     `json:"client_ip,omitempty"`
	ClientMAC               string     `json:"client_mac,omitempty"`
	CoinsInserted           int        `json:"coins_inserted"`
	TotalSeconds            int        `json:"total_seconds"`
	RemainingSeconds        int        `json:"remaining_seconds"`
	Status                  string     `json:"status"`
	StartedAt               time.Time  `json:"started_at"`
	ActivatedAt             *time.Time `json:"activated_at,omitempty"`
	ExpiredAt               *time.Time `json:"expired_at,omitempty"`
	ExpiresAt               *time.Time `json:"expires_at,omitempty"`
	PausedAt                *time.Time `json:"paused_at,omitempty"`
	RemainingSecondsAtPause *int       `json:"remaining_seconds_at_pause,omitempty"`
	ShapedMbps              *int       `json:"shaped_mbps,omitempty"`
	QdiscInfo               *QdiscInfo `json:"qdisc_info,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
}

// QdiscInfo is returned alongside each admin session row so the UI knows
// whether per-device FQ_CODEL shaping is active and what the global default is.
type QdiscInfo struct {
	Type          string `json:"type"`            // "fq_codel" | "cake" | ""
	PerDeviceMbps int    `json:"per_device_mbps"` // global per-device rate (0 = not active)
}

type SystemLog struct {
	ID        int       `json:"id"`
	Level     string    `json:"level"`
	Component string    `json:"component,omitempty"`
	Message   string    `json:"message"`
	Metadata  string    `json:"metadata,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type DailyStats struct {
	ID            int       `json:"id"`
	Date          string    `json:"date"`
	TotalEarnings int       `json:"total_earnings"`
	TotalCoins    int       `json:"total_coins"`
	TotalSessions int       `json:"total_sessions"`
	ActiveUsers   int       `json:"active_users"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ============================================
// VLAN MODELS
// ============================================
// A VLAN is network identity only (parent iface + 802.1Q id + name).
// The hotspot stack lives in the portal models below.

type VLANRequest struct {
	Interface   string `json:"interface"`
	VLANID      int    `json:"vlan_id"`
	Description string `json:"description"`
}

type VLANInfo struct {
	Interface     string `json:"interface"`
	VLANID        int    `json:"vlan_id"`
	Name          string `json:"name"`
	IP            string `json:"ip"` // live IP, informational (owned by a portal server, if any)
	Description   string `json:"description"`
	Active        bool   `json:"active"`
	HasPortal     bool   `json:"has_portal"`
	PortalEnabled bool   `json:"portal_enabled"`
}

type InterfaceInfo struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	State string `json:"state"`
}

// ============================================
// PORTAL SERVER MODELS
// ============================================

type PortalRequest struct {
	Interface string `json:"interface"`
	IPCIDR    string `json:"ip_cidr"`
	DHCPStart string `json:"dhcp_start"`
	DHCPEnd   string `json:"dhcp_end"`
	DHCPLease string `json:"dhcp_lease"`
}

type PortalInfo struct {
	Interface     string `json:"interface"`
	IPCIDR        string `json:"ip_cidr"`
	DHCPStart     string `json:"dhcp_start"`
	DHCPEnd       string `json:"dhcp_end"`
	DHCPLease     string `json:"dhcp_lease"`
	Enabled       bool   `json:"enabled"`
	IfaceExists   bool   `json:"iface_exists"`
	IfaceUp       bool   `json:"iface_up"`
	IPOK          bool   `json:"ip_ok"`
	DHCPActive    bool   `json:"dhcp_active"`
	CaptiveActive bool   `json:"captive_active"`
}

// ============================================
// PORTAL APPEARANCE MODELS
// ============================================
// The whole portal look is ONE JSON document stored in system_settings
// under key 'portal_appearance' (seeded by migration 009). The same
// shape is served by GET /api/portal/appearance and accepted by
// POST /api/admin/portal/appearance.

type PortalColors struct {
	Primary    string `json:"primary"`
	Accent     string `json:"accent"`
	Background string `json:"background"`
	Card       string `json:"card"`
	Text       string `json:"text"`
	Button     string `json:"button"`
	ButtonText string `json:"button_text"`
}

type PortalAppearance struct {
	Theme           string       `json:"theme"`
	Colors          PortalColors `json:"colors"`
	BackgroundImage string       `json:"background_image"`
	UpdatedAt       string       `json:"updated_at,omitempty"`
}

// ============================================
// REQUEST/RESPONSE TYPES
// ============================================

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Success bool   `json:"success"`
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
}

type StatsGroup struct {
	Earnings float64 `json:"earnings"`
	Coins    int     `json:"coins"`
	Sessions int     `json:"sessions"`
}

type WeeklyStat struct {
	Date     string `json:"date"`
	Earnings int    `json:"earnings"`
	Coins    int    `json:"coins"`
	Sessions int    `json:"sessions"`
}

type StatsResponse struct {
	Today          StatsGroup   `json:"today"`
	Total          StatsGroup   `json:"total"`
	ActiveSessions int          `json:"active_sessions"`
	SystemOnline   bool         `json:"system_online"`
	WeeklyStats    []WeeklyStat `json:"weekly_stats,omitempty"`
}

type SessionStatusResponse struct {
	HasActive        bool     `json:"has_active"`
	Session          *Session `json:"session,omitempty"`
	RemainingSeconds int      `json:"remaining_seconds"`
	CoinsInserted    int      `json:"coins_inserted"`
}

type GPIOConfigRequest struct {
	Pin        int    `json:"pin"`
	CoinValue  int    `json:"coin_value"`
	PulseMode  string `json:"pulse_mode"`
	BoardModel string `json:"board_model"`
}

type CoinEventRequest struct {
	CoinValue int `json:"coin_value"`
}

type PricingRequest struct {
	CoinValue int   `json:"coin_value"`
	Minutes   int   `json:"minutes"`
	Active    *bool `json:"active,omitempty"`
}

type SettingsRequest struct {
	Settings map[string]string `json:"settings"`
}

// ============================================
// WAN CONFIG MODELS
// ============================================

type WANStaticConfig struct {
	IP      string `json:"ip"`
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway"`
	DNS1    string `json:"dns1"`
	DNS2    string `json:"dns2"`
}

type WANConfig struct {
	Mode         string           `json:"mode"` // "dhcp", "static", "vlan_dhcp"
	StaticConfig *WANStaticConfig `json:"static_config,omitempty"`
	VLANID       *int             `json:"vlan_id,omitempty"`
}

type WANRequest struct {
	Mode         string           `json:"mode"`
	StaticConfig *WANStaticConfig `json:"static_config,omitempty"`
	VLANID       *int             `json:"vlan_id,omitempty"`
	ApplyToOS    bool             `json:"apply_to_os"`
}

type WANAvailableVLAN struct {
	VLANID int    `json:"vlan_id"`
	Iface  string `json:"iface"`
}

type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

type PaginatedResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data"`
	Total   int         `json:"total"`
	Limit   int         `json:"limit"`
	Offset  int         `json:"offset"`
}
