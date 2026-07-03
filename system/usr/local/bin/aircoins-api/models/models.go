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
	ID        int       `json:"id"`
	Pin       int       `json:"pin"`
	CoinValue int       `json:"coin_value"`
	PulseMode string    `json:"pulse_mode"`
	UpdatedAt time.Time `json:"updated_at"`
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
	ID               int        `json:"id"`
	ClientIP         string     `json:"client_ip,omitempty"`
	ClientMAC        string     `json:"client_mac,omitempty"`
	CoinsInserted    int        `json:"coins_inserted"`
	TotalSeconds     int        `json:"total_seconds"`
	RemainingSeconds int        `json:"remaining_seconds"`
	Status           string     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
	ExpiredAt        *time.Time `json:"expired_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
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

type StatsResponse struct {
	TodayEarnings  float64 `json:"today_earnings"`
	TodayCoins     int     `json:"today_coins"`
	TodaySessions  int     `json:"today_sessions"`
	ActiveSessions int     `json:"active_sessions"`
	TotalEarnings  float64 `json:"total_earnings"`
	TotalCoins     int     `json:"total_coins"`
	TotalSessions  int     `json:"total_sessions"`
	SystemOnline   bool    `json:"system_online"`
}

type SessionStatusResponse struct {
	HasActive        bool    `json:"has_active"`
	Session          *Session `json:"session,omitempty"`
	RemainingSeconds int     `json:"remaining_seconds"`
	CoinsInserted    int     `json:"coins_inserted"`
}

type GPIOConfigRequest struct {
	Pin       int    `json:"pin"`
	CoinValue int    `json:"coin_value"`
	PulseMode string `json:"pulse_mode"`
}

type CoinEventRequest struct {
	CoinValue int `json:"coin_value"`
}

type PricingRequest struct {
	CoinValue int `json:"coin_value"`
	Minutes   int `json:"minutes"`
}

type SettingsRequest struct {
	Settings map[string]string `json:"settings"`
}

type APIResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}
