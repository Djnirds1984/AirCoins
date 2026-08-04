package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SystemHandler struct {
	DB *sql.DB
}

// ============================================
// SERVICE STATUS CACHE
// ============================================

type serviceInfo struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

type servicesCache struct {
	mu        sync.Mutex
	data      []serviceInfo
	fetchedAt time.Time
}

var svcCache = &servicesCache{}

var knownServices = []struct {
	Name        string
	Description string
}{
	{"lighttpd", "Web Server"},
	{"aircoins-api", "API Server"},
	{"gpio-coin-listener", "GPIO Coin Listener"},
	{"pisowifi-session", "Session Manager"},
}

// GetServices returns the status of all monitored systemd services.
func (h *SystemHandler) GetServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	svcCache.mu.Lock()
	if time.Since(svcCache.fetchedAt) < 10*time.Second && svcCache.data != nil {
		cached := svcCache.data
		svcCache.mu.Unlock()
		sendJSON(w, http.StatusOK, map[string]interface{}{"services": cached})
		return
	}
	svcCache.mu.Unlock()

	// Fetch fresh data
	var result []serviceInfo
	for _, svc := range knownServices {
		status := "inactive"
		out, err := exec.Command("systemctl", "is-active", svc.Name).Output()
		if err == nil {
			status = strings.TrimSpace(string(out))
		}
		result = append(result, serviceInfo{
			Name:        svc.Name,
			Status:      status,
			Description: svc.Description,
		})
	}

	svcCache.mu.Lock()
	svcCache.data = result
	svcCache.fetchedAt = time.Now()
	svcCache.mu.Unlock()

	sendJSON(w, http.StatusOK, map[string]interface{}{"services": result})
}

// ControlService starts, stops, or restarts a whitelisted systemd service.
func (h *SystemHandler) ControlService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse path: /api/system/services/{name}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/api/system/services/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid path. Use /api/system/services/{name}/{action}"})
		return
	}

	serviceName := parts[0]
	action := parts[1]

	// Whitelist services
	allowed := map[string]bool{
		"lighttpd":           true,
		"aircoins-api":       true,
		"gpio-coin-listener": true,
		"pisowifi-session":   true,
	}
	if !allowed[serviceName] {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Unknown service: " + serviceName})
		return
	}

	// Whitelist actions
	allowedActions := map[string]bool{"start": true, "stop": true, "restart": true}
	if !allowedActions[action] {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid action: " + action + ". Allowed: start, stop, restart"})
		return
	}

	cmd := exec.Command("systemctl", action, serviceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("systemctl %s %s failed: %v — %s", action, serviceName, err, string(output))
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to %s %s: %s", action, serviceName, string(output)),
		})
		return
	}

	// Invalidate cache so next GetServices fetches fresh data
	svcCache.mu.Lock()
	svcCache.fetchedAt = time.Time{}
	svcCache.mu.Unlock()

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"service": serviceName,
		"action":  action,
	})
}

// ============================================
// SYSTEM INFO
// ============================================

// GetSystemInfo returns real system information from /proc and os.
func (h *SystemHandler) GetSystemInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	info := map[string]interface{}{}

	// Hostname
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	info["hostname"] = hostname

	// Board model
	info["board_model"] = readBoardModel()

	// Uptime
	uptimeSec, uptimeStr := readUptime()
	info["uptime"] = uptimeStr
	info["uptime_seconds"] = uptimeSec

	// Disk usage
	info["disk"] = readDiskUsage()

	// Memory
	info["memory"] = readMemory()

	// Load average
	info["load_avg"] = readLoadAvg()

	sendJSON(w, http.StatusOK, info)
}

func readBoardModel() string {
	// Try /proc/device-tree/model first (ARM boards)
	data, err := os.ReadFile("/proc/device-tree/model")
	if err == nil {
		model := strings.TrimSpace(strings.TrimRight(string(data), "\x00"))
		if model != "" {
			return model
		}
	}

	// Try /etc/os-release for PRETTY_NAME
	data, err = os.ReadFile("/etc/os-release")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				val := strings.TrimPrefix(line, "PRETTY_NAME=")
				val = strings.Trim(val, "\"")
				return val
			}
		}
	}

	return "Unknown"
}

func readUptime() (int64, string) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, "unknown"
	}
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, "unknown"
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, "unknown"
	}
	totalSec := int64(secs)
	days := totalSec / 86400
	hours := (totalSec % 86400) / 3600
	mins := (totalSec % 3600) / 60
	return totalSec, fmt.Sprintf("%dd %dh %dm", days, hours, mins)
}

func readDiskUsage() map[string]interface{} {
	out, err := exec.Command("df", "-B1", "/").Output()
	if err != nil {
		return map[string]interface{}{"total": "?", "used": "?", "available": "?", "percent": "?"}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return map[string]interface{}{"total": "?", "used": "?", "available": "?", "percent": "?"}
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return map[string]interface{}{"total": "?", "used": "?", "available": "?", "percent": "?"}
	}

	total, _ := strconv.ParseInt(fields[1], 10, 64)
	used, _ := strconv.ParseInt(fields[2], 10, 64)
	avail, _ := strconv.ParseInt(fields[3], 10, 64)

	return map[string]interface{}{
		"total":     formatBytes(total),
		"used":      formatBytes(used),
		"available": formatBytes(avail),
		"percent":   fields[4],
	}
}

func formatBytes(b int64) string {
	const gb = 1073741824
	const mb = 1048576
	if b >= gb {
		return fmt.Sprintf("%.1fG", float64(b)/float64(gb))
	}
	return fmt.Sprintf("%.1fM", float64(b)/float64(mb))
}

func readMemory() map[string]interface{} {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return map[string]interface{}{"total_mb": 0, "used_mb": 0, "available_mb": 0, "percent": "0%"}
	}

	var memTotal, memAvailable int64
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			memTotal = parseMemInfoValue(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			memAvailable = parseMemInfoValue(line)
		}
	}

	used := memTotal - memAvailable
	pct := int64(0)
	if memTotal > 0 {
		pct = (used * 100) / memTotal
	}

	return map[string]interface{}{
		"total_mb":     memTotal / 1024,
		"used_mb":      used / 1024,
		"available_mb": memAvailable / 1024,
		"percent":      fmt.Sprintf("%d%%", pct),
	}
}

func parseMemInfoValue(line string) int64 {
	// "MemTotal:       1024000 kB"
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		v, _ := strconv.ParseInt(fields[1], 10, 64)
		return v
	}
	return 0
}

func readLoadAvg() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return []float64{0, 0, 0}
	}
	fields := strings.Fields(string(data))
	result := make([]float64, 3)
	for i := 0; i < 3 && i < len(fields); i++ {
		result[i], _ = strconv.ParseFloat(fields[i], 64)
	}
	return result
}

// ============================================
// BOARD INFO
// ============================================

// GetBoardInfo returns board detection and GPIO capability info.
func (h *SystemHandler) GetBoardInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Execute the shared GPIO library to get board info
	cmd := exec.Command("bash", "-c",
		`source /usr/local/bin/aircoins-gpio-lib && `+
			`detect_board && get_gpio_method && `+
			`echo "$BOARD_FAMILY" && echo "$BOARD_MODEL" && echo "$BOARD_CHIPSET" && `+
			`echo "$GPIO_METHOD" && echo "$GPIO_PIN_COUNT" && `+
			`get_available_pins`)

	out, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to run gpio-lib detection: %v", err)
		// Fallback: read /proc/device-tree/model directly
		boardModel := readBoardModel()
		sendJSON(w, http.StatusOK, map[string]interface{}{
			"board_model":    boardModel,
			"board_family":   "unknown",
			"board_chipset":  "unknown",
			"gpio_method":    "none",
			"gpio_pin_count": 40,
			"available_pins": []int{},
		})
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 6 {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Unexpected gpio-lib output"})
		return
	}

	boardFamily := strings.TrimSpace(lines[0])
	boardModel := strings.TrimSpace(lines[1])
	boardChipset := strings.TrimSpace(lines[2])
	gpioMethod := strings.TrimSpace(lines[3])
	gpioPinCount := 40
	if n, err := strconv.Atoi(strings.TrimSpace(lines[4])); err == nil {
		gpioPinCount = n
	}

	// Parse available pins
	var availablePins []int
	if len(lines) > 5 {
		for _, s := range strings.Fields(strings.TrimSpace(lines[5])) {
			if n, err := strconv.Atoi(s); err == nil {
				availablePins = append(availablePins, n)
			}
		}
	}
	if availablePins == nil {
		availablePins = []int{}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"board_model":    boardModel,
		"board_family":   boardFamily,
		"board_chipset":  boardChipset,
		"gpio_method":    gpioMethod,
		"gpio_pin_count": gpioPinCount,
		"available_pins": availablePins,
	})
}

// ============================================
// STATUS (existing)
// ============================================

// Status returns system health check
func (h *SystemHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check database connection
	dbStatus := "ok"
	err := h.DB.Ping()
	if err != nil {
		dbStatus = "error: " + err.Error()
	}

	// Check services
	lighttpdRunning := checkServiceRunning("lighttpd")

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"services": map[string]interface{}{
			"database": dbStatus,
			"lighttpd": lighttpdRunning,
		},
		"system_online": lighttpdRunning,
	})
}

// ============================================
// LOGS (enhanced with filtering + pagination)
// ============================================

// Logs returns system logs with optional filtering and pagination.
func (h *SystemHandler) Logs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	// Parse limit (default 50, max 200)
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}

	// Parse offset (default 0)
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	level := q.Get("level")
	component := q.Get("component")

	// Build WHERE clause
	var conditions []string
	var args []interface{}
	argIdx := 1

	if level != "" {
		conditions = append(conditions, fmt.Sprintf("level = $%d", argIdx))
		args = append(args, level)
		argIdx++
	}
	if component != "" {
		conditions = append(conditions, fmt.Sprintf("component = $%d", argIdx))
		args = append(args, component)
		argIdx++
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	// Get total count
	countQuery := "SELECT COUNT(*) FROM system_logs " + whereClause
	var total int
	err := h.DB.QueryRow(countQuery, args...).Scan(&total)
	if err != nil {
		log.Printf("Error counting logs: %v", err)
		total = 0
	}

	// Fetch page
	dataQuery := fmt.Sprintf(
		`SELECT id, level, component, message, created_at
		FROM system_logs
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`,
		whereClause, argIdx, argIdx+1,
	)
	args = append(args, limit, offset)

	rows, err := h.DB.Query(dataQuery, args...)
	if err != nil {
		log.Printf("Error fetching logs: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch logs"})
		return
	}
	defer rows.Close()

	var logs []models.SystemLog
	for rows.Next() {
		var l models.SystemLog
		if err := rows.Scan(&l.ID, &l.Level, &l.Component, &l.Message, &l.CreatedAt); err != nil {
			log.Printf("Error scanning log: %v", err)
			continue
		}
		logs = append(logs, l)
	}

	sendJSON(w, http.StatusOK, models.PaginatedResponse{
		Success: true,
		Data:    logs,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
	})
}

// ============================================
// REBOOT
// ============================================

// Reboot initiates a system reboot. Uses cmd.Start() so the HTTP response
// is sent before the server is killed by the reboot.
func (h *SystemHandler) Reboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cmd := exec.Command("sudo", "reboot")
	if err := cmd.Start(); err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"message": "Failed to initiate reboot: " + err.Error(),
		})
		return
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "System is rebooting...",
	})
}

// checkServiceRunning checks if a systemd service is running
func checkServiceRunning(service string) bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", service)
	err := cmd.Run()
	return err == nil
}

// ============================================
// NTP TIME MANAGEMENT
// ============================================

// countryTimezones maps user-friendly country/region labels to IANA timezone IDs.
var countryTimezones = []map[string]string{
	{"label": "UTC (Coordinated Universal Time)", "tz": "UTC"},
	{"label": "Philippines (Manila)", "tz": "Asia/Manila"},
	{"label": "Thailand (Bangkok)", "tz": "Asia/Bangkok"},
	{"label": "Vietnam (Ho Chi Minh)", "tz": "Asia/Ho_Chi_Minh"},
	{"label": "Indonesia (Jakarta)", "tz": "Asia/Jakarta"},
	{"label": "Singapore", "tz": "Asia/Singapore"},
	{"label": "Malaysia (Kuala Lumpur)", "tz": "Asia/Kuala_Lumpur"},
	{"label": "India (Kolkata)", "tz": "Asia/Kolkata"},
	{"label": "China (Shanghai)", "tz": "Asia/Shanghai"},
	{"label": "Hong Kong", "tz": "Asia/Hong_Kong"},
	{"label": "Taiwan (Taipei)", "tz": "Asia/Taipei"},
	{"label": "Japan (Tokyo)", "tz": "Asia/Tokyo"},
	{"label": "South Korea (Seoul)", "tz": "Asia/Seoul"},
	{"label": "Australia (Sydney)", "tz": "Australia/Sydney"},
	{"label": "Australia (Perth)", "tz": "Australia/Perth"},
	{"label": "New Zealand (Auckland)", "tz": "Pacific/Auckland"},
	{"label": "United Arab Emirates (Dubai)", "tz": "Asia/Dubai"},
	{"label": "Saudi Arabia (Riyadh)", "tz": "Asia/Riyadh"},
	{"label": "Turkey (Istanbul)", "tz": "Europe/Istanbul"},
	{"label": "United Kingdom (London)", "tz": "Europe/London"},
	{"label": "Germany (Berlin)", "tz": "Europe/Berlin"},
	{"label": "France (Paris)", "tz": "Europe/Paris"},
	{"label": "Russia (Moscow)", "tz": "Europe/Moscow"},
	{"label": "Egypt (Cairo)", "tz": "Africa/Cairo"},
	{"label": "Nigeria (Lagos)", "tz": "Africa/Lagos"},
	{"label": "South Africa (Johannesburg)", "tz": "Africa/Johannesburg"},
	{"label": "United States (New York)", "tz": "America/New_York"},
	{"label": "United States (Chicago)", "tz": "America/Chicago"},
	{"label": "United States (Denver)", "tz": "America/Denver"},
	{"label": "United States (Los Angeles)", "tz": "America/Los_Angeles"},
	{"label": "Canada (Toronto)", "tz": "America/Toronto"},
	{"label": "Mexico (Mexico City)", "tz": "America/Mexico_City"},
	{"label": "Brazil (Sao Paulo)", "tz": "America/Sao_Paulo"},
	{"label": "Argentina (Buenos Aires)", "tz": "America/Argentina/Buenos_Aires"},
}

// NTPGet returns the current time, NTP sync status, timezone, and configured NTP servers.
func (h *SystemHandler) NTPGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get current timezone
	currentTZ := "UTC"
	if out, err := exec.Command("timedatectl", "show", "-p", "Timezone", "--value").Output(); err == nil {
		if tz := strings.TrimSpace(string(out)); tz != "" {
			currentTZ = tz
		}
	}

	// Check if NTP is enabled via timedatectl
	ntpEnabled := false
	ntpSynced := false
	if out, err := exec.Command("timedatectl", "show").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "NTP=") {
				ntpEnabled = strings.TrimSpace(strings.TrimPrefix(line, "NTP=")) == "yes"
			}
			if strings.HasPrefix(line, "NTPSynchronized=") {
				ntpSynced = strings.TrimSpace(strings.TrimPrefix(line, "NTPSynchronized=")) == "yes"
			}
		}
	}

	// Get configured NTP servers from /etc/systemd/timesyncd.conf
	ntpServers := getNTPServers()

	// Get last sync time if available
	lastSync := ""
	if out, err := exec.Command("timedatectl", "show", "-p", "TimeSyncTimestamp", "--value").Output(); err == nil {
		ts := strings.TrimSpace(string(out))
		if ts != "" && ts != "n/a" {
			lastSync = ts
		}
	}

	// Current time in the configured timezone
	now := time.Now()

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":          true,
		"current_time":     now.Format("2006-01-02 15:04:05"),
		"current_timezone": currentTZ,
		"timezone":         now.Location().String(),
		"unix_timestamp":   now.Unix(),
		"ntp_enabled":      ntpEnabled,
		"ntp_synced":       ntpSynced,
		"ntp_servers":      ntpServers,
		"last_sync":        lastSync,
	})
}

// NTPSet configures the timezone, NTP servers, and enables/disables NTP sync.
func (h *SystemHandler) NTPSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Servers  []string `json:"servers"`
		Enabled  *bool    `json:"enabled"`
		Timezone string   `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	// Set timezone if provided
	if req.Timezone != "" {
		if !isValidTimezone(req.Timezone) {
			sendJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false,
				"error":   "Invalid timezone: " + req.Timezone,
			})
			return
		}
		if out, err := exec.Command("sudo", "timedatectl", "set-timezone", req.Timezone).CombinedOutput(); err != nil {
			log.Printf("set-timezone failed: %s", string(out))
			sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to set timezone: " + string(out),
			})
			return
		}
	}

	// Enable/disable NTP
	if req.Enabled != nil {
		val := "yes"
		if !*req.Enabled {
			val = "no"
		}
		if out, err := exec.Command("sudo", "timedatectl", "set-ntp", val).CombinedOutput(); err != nil {
			log.Printf("NTP set-ntp failed: %s", string(out))
			sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to set NTP: " + string(out),
			})
			return
		}
	}

	// Set NTP servers if provided
	if len(req.Servers) > 0 {
		if err := setNTPServers(req.Servers); err != nil {
			sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   "Failed to set NTP servers: " + err.Error(),
			})
			return
		}
	}

	// Restart timesyncd to apply all changes
	if req.Enabled != nil || len(req.Servers) > 0 {
		exec.Command("sudo", "systemctl", "restart", "systemd-timesyncd").Run()
		time.Sleep(2 * time.Second)
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "NTP settings updated",
	})
}

// NTPTimezones returns the curated list of country/region → timezone mappings.
func (h *SystemHandler) NTPTimezones(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"timezones": countryTimezones,
	})
}

// isValidTimezone checks if the timezone exists in the curated list.
func isValidTimezone(tz string) bool {
	for _, entry := range countryTimezones {
		if entry["tz"] == tz {
			return true
		}
	}
	// Also allow UTC
	if tz == "UTC" {
		return true
	}
	return false
}

// NTPSync forces an immediate NTP time synchronization.
func (h *SystemHandler) NTPSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Restart timesyncd to force sync
	if out, err := exec.Command("sudo", "systemctl", "restart", "systemd-timesyncd").CombinedOutput(); err != nil {
		sendJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to sync time: " + string(out),
		})
		return
	}

	// Wait a moment for sync
	time.Sleep(3 * time.Second)

	// Get updated time
	now := time.Now()

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":        true,
		"message":        "Time synchronized",
		"current_time":   now.Format("2006-01-02 15:04:05"),
		"unix_timestamp": now.Unix(),
	})
}

// getNTPServers reads the configured NTP servers from timesyncd.conf
func getNTPServers() []string {
	data, err := os.ReadFile("/etc/systemd/timesyncd.conf")
	if err != nil {
		return []string{}
	}

	var servers []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "NTP=") {
			// NTP=pool.ntp.org time.google.com
			val := strings.TrimPrefix(line, "NTP=")
			for _, s := range strings.Fields(val) {
				if s != "" {
					servers = append(servers, s)
				}
			}
		}
	}
	return servers
}

// setNTPServers writes the NTP servers to timesyncd.conf
func setNTPServers(servers []string) error {
	confPath := "/etc/systemd/timesyncd.conf"

	// Read existing config
	data, err := os.ReadFile(confPath)
	if err != nil {
		data = []byte("[Time]\n#NTP=\n#FallbackNTP=\n")
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	var newLines []string
	ntpWritten := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Skip existing NTP= lines (commented or not)
		if strings.HasPrefix(trimmed, "NTP=") || strings.HasPrefix(trimmed, "#NTP=") {
			if !ntpWritten && len(servers) > 0 {
				newLines = append(newLines, "NTP="+strings.Join(servers, " "))
				ntpWritten = true
			}
			continue
		}
		newLines = append(newLines, line)
	}

	// If no NTP line was written yet, add it after [Time]
	if !ntpWritten && len(servers) > 0 {
		for i, line := range newLines {
			if strings.TrimSpace(line) == "[Time]" {
				// Insert after [Time]
				newLines = append(newLines[:i+1], append([]string{"NTP=" + strings.Join(servers, " ")}, newLines[i+1:]...)...)
				break
			}
		}
	}

	return os.WriteFile(confPath, []byte(strings.Join(newLines, "\n")), 0644)
}
