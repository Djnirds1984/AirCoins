package main

import (
	"aircoins-api/handlers"
	"aircoins-api/models"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"
)

var Version = "dev"

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

	// ── License system ──────────────────────────────────────────────────
	licenseHandler := handlers.NewLicenseHandler(models.DB)
	if err := licenseHandler.InitOrLoadLicense(); err != nil {
		log.Printf("WARNING: license init failed: %v", err)
	}
	go func() {
		if err := licenseHandler.HeartbeatSupabase(); err != nil {
			log.Printf("WARNING: initial heartbeat failed: %v", err)
		}
	}()
	var scheduleHeartbeat func()
	scheduleHeartbeat = func() {
		time.AfterFunc(24*time.Hour, func() {
			if err := licenseHandler.HeartbeatSupabase(); err != nil {
				log.Printf("WARNING: heartbeat failed: %v", err)
			}
			scheduleHeartbeat()
		})
	}
	scheduleHeartbeat()

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

	// Inject LicenseHandler into SessionHandler so /api/status can report
	// whether the license is valid to portal clients.
	sessionHandler.LicenseHandler = licenseHandler

	// Setup routes
	mux := http.NewServeMux()

	// adminProtected wraps an admin handler with both the license gate and
	// auth middleware: LicenseGate → Auth → handler.
	adminProtected := func(hf http.HandlerFunc) http.Handler {
		return handlers.LicenseGateMiddleware(licenseHandler, handlers.AuthMiddleware(http.HandlerFunc(hf)))
	}

	// ── Admin routes (license-gated + auth) ──────────────────────────────
	mux.HandleFunc("/api/admin/login", adminHandler.Login) // no gate, no auth
	mux.Handle("/api/admin/stats", adminProtected(adminHandler.GetStats))
	mux.Handle("/api/admin/sessions", adminProtected(adminHandler.GetSessions))
	mux.Handle("/api/admin/sessions/", adminProtected(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("/api/admin/settings", adminProtected(adminHandler.Settings))
	mux.Handle("/api/admin/logs", adminProtected(adminHandler.GetLogs))
	mux.Handle("/api/admin/coin-events", adminProtected(adminHandler.GetCoinEvents))
	mux.Handle("/api/admin/system/stats", adminProtected(systemHandler.GetSystemStats))

	// Reports routes
	mux.Handle("/api/admin/reports/earnings", adminProtected(reportsHandler.GetEarningsSummary))
	mux.Handle("/api/admin/reports/daily", adminProtected(reportsHandler.GetDailyBreakdown))
	mux.Handle("/api/admin/reports/coin-events", adminProtected(reportsHandler.GetCoinEvents))

	// ── License endpoints (auth only, NO license gate) ──────────────────
	mux.Handle("/api/admin/license/status", handlers.AuthMiddleware(http.HandlerFunc(licenseHandler.Status)))
	mux.Handle("/api/admin/license/activate", handlers.AuthMiddleware(http.HandlerFunc(licenseHandler.Activate)))
	mux.Handle("/api/admin/license/deactivate", handlers.AuthMiddleware(http.HandlerFunc(licenseHandler.Deactivate)))

	// ── ZeroTier endpoints (auth + license gate) ─────────────────────────
	mux.Handle("/api/admin/zerotier/status", adminProtected(handlers.ZeroTierGetStatus))
	mux.Handle("/api/admin/zerotier/install", adminProtected(handlers.ZeroTierInstall))
	mux.Handle("/api/admin/zerotier/join", adminProtected(handlers.ZeroTierJoin))
	mux.Handle("/api/admin/zerotier/leave", adminProtected(handlers.ZeroTierLeave))

	// Session routes
	// start/status/current are PUBLIC (portal-facing): the caller is
	// identified by its own IP/MAC and credited only from unprocessed
	// coin_events, so a client cannot grant itself time.
	// extend/end/create mutate arbitrary sessions -> admin auth required.
	mux.HandleFunc("/api/session/current", sessionHandler.GetCurrent)
	mux.HandleFunc("/api/session/start", sessionHandler.Start)
	mux.HandleFunc("/api/session/can-start", sessionHandler.CanStart)
	mux.Handle("/api/session/extend", adminProtected(sessionHandler.Extend))
	mux.Handle("/api/session/end", adminProtected(sessionHandler.End))
	mux.HandleFunc("/api/session/status", sessionHandler.Status)
	mux.HandleFunc("/api/session/pause", sessionHandler.Pause)
	mux.HandleFunc("/api/session/resume", sessionHandler.Resume)
	mux.HandleFunc("/api/session/ban-status", sessionHandler.BanStatus)
	mux.Handle("/api/admin/session/create", adminProtected(sessionHandler.AdminCreate))

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
	mux.Handle("/api/system/board", adminProtected(systemHandler.GetBoardInfo))
	mux.Handle("/api/system/logs", adminProtected(systemHandler.Logs))
	mux.Handle("/api/system/info", adminProtected(systemHandler.GetSystemInfo))
	mux.Handle("/api/system/services", adminProtected(systemHandler.GetServices))
	mux.Handle("/api/system/services/", adminProtected(systemHandler.ControlService))
	mux.Handle("/api/admin/system/reboot", adminProtected(systemHandler.Reboot))

	// VLAN routes (network identity only — create/delete the 802.1Q iface)
	mux.Handle("/api/vlan/list", adminProtected(handlers.VLANList))
	mux.Handle("/api/vlan/create", adminProtected(handlers.VLANCreate))
	mux.Handle("/api/vlan/delete", adminProtected(handlers.VLANDelete))
	mux.Handle("/api/vlan/interfaces", adminProtected(handlers.VLANInterfaces))

	// WAN settings routes (auto-detect WAN port, DHCP/Static/VLAN DHCP modes)
	mux.Handle("/api/admin/wan", adminProtected(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handlers.WANGet(w, r)
		case http.MethodPost:
			handlers.WANPost(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.Handle("/api/admin/wan/available-vlans", adminProtected(handlers.WANAvailableVLANs))

	// Portal server routes (hotspot stack: portal IP + DHCP + DNS hijack
	// + captive rules on a chosen interface)
	mux.Handle("/api/portal/list", adminProtected(handlers.PortalList))
	mux.Handle("/api/portal/create", adminProtected(handlers.PortalCreate))
	mux.Handle("/api/portal/enable", adminProtected(handlers.PortalEnable))
	mux.Handle("/api/portal/disable", adminProtected(handlers.PortalDisable))
	mux.Handle("/api/portal/delete", adminProtected(handlers.PortalDelete))

	// Portal appearance routes (theme/colors/background image)
	// GET appearance is PUBLIC — the captive portal reads it on load.
	// Everything that mutates the appearance requires admin auth.
	mux.HandleFunc("/api/portal/appearance", appearanceHandler.GetAppearance)
	mux.Handle("/api/admin/portal/appearance", adminProtected(appearanceHandler.SaveAppearance))
	mux.Handle("/api/admin/portal/background", adminProtected(appearanceHandler.Background))

	// Portal tap/pause rules + bans
	mux.HandleFunc("/api/portal/tap-rules", appearanceHandler.GetTapRules)
	mux.Handle("/api/admin/portal/tap-rules", adminProtected(appearanceHandler.HandleTapRules))
	mux.Handle("/api/admin/portal/pause-rules", adminProtected(appearanceHandler.HandlePauseRules))
	mux.Handle("/api/admin/portal/bans", adminProtected(appearanceHandler.ListBans))
	mux.Handle("/api/admin/portal/ban/", adminProtected(appearanceHandler.UnbanByPath))

	// Portal qdisc (traffic shaping: CAKE / FQ_CODEL)
	mux.Handle("/api/admin/portal/qdisc/apply", adminProtected(qdiscHandler.ApplyQdisc))
	mux.Handle("/api/admin/portal/qdiag/installed", adminProtected(qdiscHandler.QDiagInstalled))
	mux.Handle("/api/admin/portal/qdiag", adminProtected(qdiscHandler.QDiag))
	mux.Handle("/api/admin/portal/qdisc", adminProtected(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("/api/admin/audio/", adminProtected(audioHandler.AdminDispatch))
	mux.Handle("/api/admin/audio", adminProtected(audioHandler.AdminList))

	// Public audio serve (no auth — the captive portal fetches these
	// without a Bearer token).
	mux.HandleFunc("/audio/", audioHandler.Serve)

	// Updater route (check for new AirCoins releases on GitHub)
	updaterHandler := handlers.NewUpdaterHandler()
	mux.Handle("/api/admin/updater/check", adminProtected(updaterHandler.CheckForUpdate))

	// Health check
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, Version)
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

	handlers.SetAPIVersion(Version)

	log.Printf("AirCoins API %s starting on %s", Version, addr)

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
		w.Header().Set("Access-Control-Expose-Headers", "X-Version")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
