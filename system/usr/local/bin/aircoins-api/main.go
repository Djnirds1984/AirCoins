package main

import (
	"aircoins-api/handlers"
	"aircoins-api/models"
	"log"
	"net/http"
	"os"
	"os/exec"
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

	// Startup check: warn if tc (iproute2) is not installed.
	// Traffic shaping will silently not work without it.
	if tcPath, err := exec.LookPath("tc"); err != nil {
		log.Printf("WARNING: tc command not found — traffic shaping (qdisc) will not work. Install iproute2: apt install iproute2")
	} else {
		log.Printf("tc found at %s — traffic shaping available", tcPath)
	}

	// One-time flat-file migration for the VLAN/portal split: moves any
	// inline portal config left in vlans.conf into portals.conf so
	// existing portals keep working after the upgrade (DB side is
	// handled by migrations.sql 007).
	handlers.MigrateLegacyPortalConfig()

	// Ensure the audio upload directory exists.
	if err := handlers.EnsureAudioDir(); err != nil {
		log.Printf("WARNING: audio directory not available: %v", err)
	}

	// Initialize handlers
	adminHandler := &handlers.AdminHandler{DB: models.DB}
	sessionHandler := &handlers.SessionHandler{DB: models.DB}
	gpioHandler := &handlers.GPIOHandler{DB: models.DB}
	pricingHandler := &handlers.PricingHandler{DB: models.DB}
	systemHandler := &handlers.SystemHandler{DB: models.DB}
	reportsHandler := &handlers.ReportsHandler{DB: models.DB}
	coinslotHandler := &handlers.CoinslotHandler{DB: models.DB}
	appearanceHandler := &handlers.AppearanceHandler{DB: models.DB}
	qdiscHandler := &handlers.QdiscHandler{DB: models.DB}
	sessionAdminHandler := &handlers.SessionAdminHandler{DB: models.DB}

	// Setup routes
	mux := http.NewServeMux()

	// Admin routes
	mux.HandleFunc("/api/admin/login", adminHandler.Login)
	mux.HandleFunc("/api/admin/stats", handlers.AuthMiddleware(adminHandler.GetStats))
	mux.HandleFunc("/api/admin/sessions", handlers.AuthMiddleware(adminHandler.GetSessions))
	mux.HandleFunc("/api/admin/sessions/", handlers.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Dispatch /api/admin/sessions/<id>/shape to the Shape handler;
		// PATCH and DELETE go to the session admin CRUD handler;
		// GET for individual session goes to session admin (detail + coin_events).
		if len(r.URL.Path) > len("/api/admin/sessions/") && r.URL.Path[len(r.URL.Path)-len("/shape"):] == "/shape" {
			sessionHandler.Shape(w, r)
			return
		}
		if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			sessionAdminHandler.Dispatch(w, r)
			return
		}
		sessionAdminHandler.Dispatch(w, r)
	}))
	mux.HandleFunc("/api/admin/settings", handlers.AuthMiddleware(adminHandler.Settings))
	mux.HandleFunc("/api/admin/logs", handlers.AuthMiddleware(adminHandler.GetLogs))
	mux.HandleFunc("/api/admin/coin-events", handlers.AuthMiddleware(adminHandler.GetCoinEvents))
	mux.HandleFunc("/api/admin/system/stats", handlers.AuthMiddleware(systemHandler.GetSystemStats))

	// Reports routes
	mux.HandleFunc("/api/admin/reports/earnings", handlers.AuthMiddleware(reportsHandler.GetEarningsSummary))
	mux.HandleFunc("/api/admin/reports/daily", handlers.AuthMiddleware(reportsHandler.GetDailyBreakdown))
	mux.HandleFunc("/api/admin/reports/coin-events", handlers.AuthMiddleware(reportsHandler.GetCoinEvents))

	// Session routes
	// start/status/current are PUBLIC (portal-facing): the caller is
	// identified by its own IP/MAC and credited only from unprocessed
	// coin_events, so a client cannot grant itself time.
	// extend/end/create mutate arbitrary sessions -> admin auth required.
	mux.HandleFunc("/api/session/current", sessionHandler.GetCurrent)
	mux.HandleFunc("/api/session/start", sessionHandler.Start)
	mux.HandleFunc("/api/session/can-start", sessionHandler.CanStart)
	mux.HandleFunc("/api/session/extend", handlers.AuthMiddleware(sessionHandler.Extend))
	mux.HandleFunc("/api/session/end", handlers.AuthMiddleware(sessionHandler.End))
	mux.HandleFunc("/api/session/status", sessionHandler.Status)
	mux.HandleFunc("/api/session/pause", sessionHandler.Pause)
	mux.HandleFunc("/api/session/resume", sessionHandler.Resume)
	mux.HandleFunc("/api/session/ban-status", sessionHandler.BanStatus)
	mux.HandleFunc("/api/admin/session/create", handlers.AuthMiddleware(sessionHandler.AdminCreate))

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

	// VLAN routes (network identity only — create/delete the 802.1Q iface)
	mux.HandleFunc("/api/vlan/list", handlers.AuthMiddleware(handlers.VLANList))
	mux.HandleFunc("/api/vlan/create", handlers.AuthMiddleware(handlers.VLANCreate))
	mux.HandleFunc("/api/vlan/delete", handlers.AuthMiddleware(handlers.VLANDelete))
	mux.HandleFunc("/api/vlan/interfaces", handlers.AuthMiddleware(handlers.VLANInterfaces))

	// WAN settings routes (auto-detect WAN port, DHCP/Static/VLAN DHCP modes)
	mux.HandleFunc("/api/admin/wan", handlers.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handlers.WANGet(w, r)
		case http.MethodPost:
			handlers.WANPost(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/admin/wan/available-vlans", handlers.AuthMiddleware(handlers.WANAvailableVLANs))

	// Portal server routes (hotspot stack: portal IP + DHCP + DNS hijack
	// + captive rules on a chosen interface)
	mux.HandleFunc("/api/portal/list", handlers.AuthMiddleware(handlers.PortalList))
	mux.HandleFunc("/api/portal/create", handlers.AuthMiddleware(handlers.PortalCreate))
	mux.HandleFunc("/api/portal/enable", handlers.AuthMiddleware(handlers.PortalEnable))
	mux.HandleFunc("/api/portal/disable", handlers.AuthMiddleware(handlers.PortalDisable))
	mux.HandleFunc("/api/portal/delete", handlers.AuthMiddleware(handlers.PortalDelete))

	// Portal appearance routes (theme/colors/background image)
	// GET appearance is PUBLIC — the captive portal reads it on load.
	// Everything that mutates the appearance requires admin auth.
	mux.HandleFunc("/api/portal/appearance", appearanceHandler.GetAppearance)
	mux.HandleFunc("/api/admin/portal/appearance", handlers.AuthMiddleware(appearanceHandler.SaveAppearance))
	mux.HandleFunc("/api/admin/portal/background", handlers.AuthMiddleware(appearanceHandler.Background))

	// Portal tap/pause rules + bans
	mux.HandleFunc("/api/portal/tap-rules", appearanceHandler.GetTapRules)
	mux.HandleFunc("/api/admin/portal/tap-rules", handlers.AuthMiddleware(appearanceHandler.HandleTapRules))
	mux.HandleFunc("/api/admin/portal/pause-rules", handlers.AuthMiddleware(appearanceHandler.HandlePauseRules))
	mux.HandleFunc("/api/admin/portal/bans", handlers.AuthMiddleware(appearanceHandler.ListBans))
	mux.HandleFunc("/api/admin/portal/ban/", handlers.AuthMiddleware(appearanceHandler.UnbanByPath))

	// Portal qdisc (traffic shaping: CAKE / FQ_CODEL)
	mux.HandleFunc("/api/admin/portal/qdisc/apply", handlers.AuthMiddleware(qdiscHandler.ApplyQdisc))
	mux.HandleFunc("/api/admin/portal/qdiag/installed", handlers.AuthMiddleware(qdiscHandler.QDiagInstalled))
	mux.HandleFunc("/api/admin/portal/qdiag", handlers.AuthMiddleware(qdiscHandler.QDiag))
	mux.HandleFunc("/api/admin/portal/qdisc", handlers.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			qdiscHandler.GetQdisc(w, r)
		case http.MethodPost:
			qdiscHandler.SaveQdisc(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	// Admin audio routes (auth-wrapped): upload, delete, list
	audioHandler := &handlers.AudioHandler{DB: models.DB}
	mux.HandleFunc("/api/admin/audio/", handlers.AuthMiddleware(audioHandler.AdminDispatch))
	mux.HandleFunc("/api/admin/audio", handlers.AuthMiddleware(audioHandler.AdminList))

	// Public audio serve (no auth — the captive portal fetches these
	// without a Bearer token).
	mux.HandleFunc("/audio/", audioHandler.Serve)

	// Health check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"aircoins-api"}`))
	})

	// Recover iptables auth state for surviving sessions (reboot safety)
	// and enforce wall-clock expiry every 30s in the background.
	handlers.StartExpiryEnforcer(models.DB)

	// Re-apply saved qdisc (traffic shaping) rules on boot.
	// Runs in a goroutine so it doesn't block the HTTP listener;
	// includes a 30 s delayed retry for VLAN interfaces not yet up.
	handlers.ApplyQdiscOnBootWithRetry(models.DB)

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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Session-Token")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
