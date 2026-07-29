package main

import (
	"aircoins-api/handlers"
	"aircoins-api/models"
	"log"
	"net/http"
	"os"
)

func main() {
	// Database configuration
	dbConfig := models.DBConfig{
		Host:     getEnv("DB_HOST", "localhost"),
		Port:     getEnv("DB_PORT", "5432"),
		User:     getEnv("DB_USER", "aircoins"),
		Password: getEnv("DB_PASSWORD", "aircoins123"),
		DBName:   getEnv("DB_NAME", "aircoins"),
	}

	// Initialize database connection
	err := models.InitDB(dbConfig)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer models.DB.Close()

	log.Println("Connected to PostgreSQL database")

	// Initialize handlers
	adminHandler := &handlers.AdminHandler{DB: models.DB}
	sessionHandler := &handlers.SessionHandler{DB: models.DB}
	gpioHandler := &handlers.GPIOHandler{DB: models.DB}
	pricingHandler := &handlers.PricingHandler{DB: models.DB}
	systemHandler := &handlers.SystemHandler{DB: models.DB}
	reportsHandler := &handlers.ReportsHandler{DB: models.DB}
	coinslotHandler := &handlers.CoinslotHandler{}

	// Setup routes
	mux := http.NewServeMux()

	// Admin routes
	mux.HandleFunc("/api/admin/login", adminHandler.Login)
	mux.HandleFunc("/api/admin/stats", handlers.AuthMiddleware(adminHandler.GetStats))
	mux.HandleFunc("/api/admin/sessions", handlers.AuthMiddleware(adminHandler.GetSessions))
	mux.HandleFunc("/api/admin/sessions/", handlers.AuthMiddleware(adminHandler.GetSession))
	mux.HandleFunc("/api/admin/settings", handlers.AuthMiddleware(adminHandler.Settings))
	mux.HandleFunc("/api/admin/logs", handlers.AuthMiddleware(adminHandler.GetLogs))
	mux.HandleFunc("/api/admin/coin-events", handlers.AuthMiddleware(adminHandler.GetCoinEvents))

	// Reports routes
	mux.HandleFunc("/api/admin/reports/earnings", handlers.AuthMiddleware(reportsHandler.GetEarningsSummary))
	mux.HandleFunc("/api/admin/reports/daily", handlers.AuthMiddleware(reportsHandler.GetDailyBreakdown))
	mux.HandleFunc("/api/admin/reports/coin-events", handlers.AuthMiddleware(reportsHandler.GetCoinEvents))

	// Session routes
	mux.HandleFunc("/api/session/current", sessionHandler.GetCurrent)
	mux.HandleFunc("/api/session/start", sessionHandler.Start)
	mux.HandleFunc("/api/session/extend", sessionHandler.Extend)
	mux.HandleFunc("/api/session/end", sessionHandler.End)
	mux.HandleFunc("/api/session/status", sessionHandler.Status)

	// GPIO routes
	mux.HandleFunc("/api/gpio/config", gpioHandler.Config)
	mux.HandleFunc("/api/gpio/test", gpioHandler.Test)
	mux.HandleFunc("/api/gpio/coin", gpioHandler.Coin)

	// Coin slot arm/disarm routes (PUBLIC - used by captive portal customers)
	mux.HandleFunc("/api/coinslot/arm", coinslotHandler.Arm)
	mux.HandleFunc("/api/coinslot/disarm", coinslotHandler.Disarm)
	mux.HandleFunc("/api/coinslot/status", coinslotHandler.Status)

	// Pricing routes
	mux.HandleFunc("/api/pricing", pricingHandler.Handle)
	mux.HandleFunc("/api/pricing/", pricingHandler.Handle)

	// System routes
	mux.HandleFunc("/api/system/status", systemHandler.Status)
	mux.HandleFunc("/api/system/board", handlers.AuthMiddleware(systemHandler.GetBoardInfo))
	mux.HandleFunc("/api/system/logs", handlers.AuthMiddleware(systemHandler.Logs))
	mux.HandleFunc("/api/system/info", handlers.AuthMiddleware(systemHandler.GetSystemInfo))
	mux.HandleFunc("/api/system/services", handlers.AuthMiddleware(systemHandler.GetServices))
	mux.HandleFunc("/api/system/services/", handlers.AuthMiddleware(systemHandler.ControlService))

	// VLAN routes
	mux.HandleFunc("/api/vlan/list", handlers.AuthMiddleware(handlers.VLANList))
	mux.HandleFunc("/api/vlan/create", handlers.AuthMiddleware(handlers.VLANCreate))
	mux.HandleFunc("/api/vlan/delete", handlers.AuthMiddleware(handlers.VLANDelete))
	mux.HandleFunc("/api/vlan/interfaces", handlers.AuthMiddleware(handlers.VLANInterfaces))

	// Health check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"aircoins-api"}`))
	})

	// Get port from environment or use default
	port := getEnv("PORT", "8080")
	addr := ":" + port

	log.Printf("AirCoins API server starting on %s", addr)

	// Enable CORS for development
	handler := enableCORS(mux)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
