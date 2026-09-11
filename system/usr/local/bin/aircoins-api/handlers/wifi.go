package handlers

import (
	"aircoins-api/models"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// wifi.go manages a wireless adapter as an OPEN access point (hotspot)
// using hostapd. Security is intentionally passwordless — the hotspot is
// a captive-portal entry interface; the portal stack (IP, dnsmasq,
// portal_servers) is provisioned on the interface separately via the
// portal endpoints, exactly like a VLAN or bridge interface.
//
// Config files:
//   - /etc/pisowifi/hostapd.conf   — generated hostapd config (open auth)
//   - /etc/default/hostapd         — DAEMON_CONF pointed at the above
//   - wifi_ap_config DB table      — saved identity (iface, ssid, channel)

const (
	wifiHostapdConfPath = "/etc/pisowifi/hostapd.conf"
	wifiHostapdDefaults = "/etc/default/hostapd"
)

// ============================================
// KERNEL / IW HELPERS
// ============================================

// listWirelessInterfaces returns the names of all wireless interfaces
// (anything the kernel exposes a "wireless" sysfs dir for, plus the
// conventional wl*/uap* prefixes used by some drivers).
func listWirelessInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}

	var ifaces []string
	for _, e := range entries {
		name := e.Name()
		if _, err := os.Stat("/sys/class/net/" + name + "/wireless"); err == nil {
			ifaces = append(ifaces, name)
			continue
		}
		if strings.HasPrefix(name, "wl") || strings.HasPrefix(name, "uap") {
			if _, err := os.Stat("/sys/class/net/" + name); err == nil {
				ifaces = append(ifaces, name)
			}
		}
	}
	return ifaces
}

// wifiPhyIndex resolves the phy (radio) index backing an interface, e.g.
// "phy0" for wlan0. Returns "" when unknown.
func wifiPhyIndex(iface string) string {
	out, err := exec.Command("iw", "dev", iface, "info").Output()
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`wiphy\s+(\d+)`)
	if m := re.FindStringSubmatch(string(out)); m != nil {
		return "phy" + m[1]
	}
	return ""
}

// adapterSupportsAP reports whether the adapter's driver supports the AP
// (master) mode — required for hostapd to work. Many cheap USB adapters
// do not, so this must be a hard, explicit check.
func adapterSupportsAP(iface string) bool {
	phy := wifiPhyIndex(iface)
	if phy == "" {
		return false
	}
	out, err := exec.Command("iw", phy, "info").Output()
	if err != nil {
		return false
	}
	// "Supported interface modes:" block lists entries like "* AP"
	inBlock := false
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Supported interface modes:") {
			inBlock = true
			continue
		}
		if inBlock {
			if !strings.HasPrefix(trimmed, "*") {
				break // block ended
			}
			if trimmed == "* AP" || trimmed == "* AP/VLAN" {
				return true
			}
		}
	}
	return false
}

// wifiChannelsFor returns the channels (per band) the adapter supports,
// parsed from `iw phy<N> info`. Disabled/regulatory-blocked channels are
// excluded so the UI never offers one hostapd would refuse.
func wifiChannelsFor(iface string) []models.WiFiChannel {
	phy := wifiPhyIndex(iface)
	if phy == "" {
		return nil
	}
	out, err := exec.Command("iw", phy, "info").Output()
	if err != nil {
		return nil
	}

	re := regexp.MustCompile(`\*\s+(\d+)\.0\s+MHz\s+\[(\d+)\]`)
	seen := make(map[int]bool)
	var channels []models.WiFiChannel
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "disabled") {
			continue
		}
		m := re.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		freq, _ := strconv.Atoi(m[1])
		ch, _ := strconv.Atoi(m[2])
		if seen[ch] {
			continue
		}
		seen[ch] = true
		band := "2.4GHz"
		if freq >= 3000 {
			band = "5GHz"
		}
		channels = append(channels, models.WiFiChannel{
			Channel: ch,
			FreqMHz: freq,
			Band:    band,
		})
	}
	return channels
}

// channelSupported checks a requested channel against the adapter's list.
func channelSupported(channels []models.WiFiChannel, ch int) bool {
	for _, c := range channels {
		if c.Channel == ch {
			return true
		}
	}
	return false
}

// hwModeForChannel picks the hostapd hw_mode for a channel's band.
func hwModeForChannel(channels []models.WiFiChannel, ch int) string {
	for _, c := range channels {
		if c.Channel == ch {
			if c.Band == "5GHz" {
				return "a"
			}
			return "g"
		}
	}
	return "g"
}

// hostapdInstalled checks whether the hostapd binary exists.
func hostapdInstalled() bool {
	out, err := exec.Command("sh", "-c", "command -v hostapd").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// hostapdRunning reports whether the hostapd service is active.
func hostapdRunning() bool {
	if err := exec.Command("systemctl", "is-active", "--quiet", "hostapd").Run(); err != nil {
		// is-active --quiet exits non-zero when inactive; fall back to pgrep
		return exec.Command("pgrep", "-x", "hostapd").Run() == nil
	}
	return true
}

// apStationCount returns the number of associated stations (clients).
func apStationCount(iface string) int {
	out, err := exec.Command("iw", "dev", iface, "station", "dump").Output()
	if err != nil {
		return 0
	}
	return strings.Count(string(out), "Station ")
}

// ============================================
// HOSTAPD CONFIG GENERATION
// ============================================

// writeHostapdConfig generates the open-mode hostapd config and wires it
// into /etc/default/hostapd (DAEMON_CONF) so the stock hostapd.service
// picks it up on boot. No WPA keys are ever written: the hotspot is an
// open captive-portal entry by design.
func writeHostapdConfig(cfg models.WiFiAPConfig) error {
	if err := os.MkdirAll(filepath.Dir(wifiHostapdConfPath), 0755); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# Generated by aircoins-api — do not edit by hand.\n")
	b.WriteString("# OPEN hotspot (no password): managed from the admin Wi-Fi Hotspot page.\n")
	b.WriteString("interface=" + cfg.Interface + "\n")
	b.WriteString("driver=nl80211\n")
	b.WriteString("ssid=" + cfg.SSID + "\n")
	b.WriteString("country_code=" + cfg.CountryCode + "\n")
	b.WriteString("channel=" + strconv.Itoa(cfg.Channel) + "\n")
	b.WriteString("hw_mode=" + cfg.HwMode + "\n")
	// Open authentication: no WPA/RSN lines at all.
	b.WriteString("auth_algs=1\n")
	// Log to the journal, not a private syslog facility.
	b.WriteString("logger_syslog=-1\n")
	b.WriteString("logger_syslog_level=2\n")
	// Multicast-to-unicast helps low-power client radios.
	b.WriteString("multicast_to_unicast=1\n")

	if err := os.WriteFile(wifiHostapdConfPath, []byte(b.String()), 0644); err != nil {
		return err
	}

	// Point the stock hostapd.service at our config (best-effort).
	defaults, err := os.ReadFile(wifiHostapdDefaults)
	content := ""
	if err == nil {
		content = string(defaults)
	}
	// Replace any existing DAEMON_CONF= line, or append one.
	re := regexp.MustCompile(`(?m)^DAEMON_CONF=.*$`)
	line := "DAEMON_CONF=\"" + wifiHostapdConfPath + "\""
	if re.MatchString(content) {
		content = re.ReplaceAllString(content, line)
	} else {
		content += "\n" + line + "\n"
	}
	if err := os.WriteFile(wifiHostapdDefaults, []byte(content), 0644); err != nil {
		log.Printf("wifi: warning: could not update %s: %v", wifiHostapdDefaults, err)
	}
	return nil
}

// ============================================
// DB HELPERS
// ============================================

// readWiFiAPConfig returns the saved hotspot config for an interface.
func readWiFiAPConfig(iface string) (models.WiFiAPConfig, bool) {
	var cfg models.WiFiAPConfig
	err := models.DB.QueryRow(
		"SELECT interface, ssid, channel, hw_mode, country_code, enabled FROM wifi_ap_config WHERE interface = $1",
		iface,
	).Scan(&cfg.Interface, &cfg.SSID, &cfg.Channel, &cfg.HwMode, &cfg.CountryCode, &cfg.Enabled)
	if err != nil {
		return models.WiFiAPConfig{}, false
	}
	return cfg, true
}

// currentHostapdIface determines which interface hostapd is currently
// serving (from the generated config, falling back to the DB).
func currentHostapdIface() (models.WiFiAPConfig, bool) {
	data, err := os.ReadFile(wifiHostapdConfPath)
	if err != nil {
		return models.WiFiAPConfig{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface=") {
			iface := strings.TrimPrefix(line, "interface=")
			if cfg, ok := readWiFiAPConfig(iface); ok {
				return cfg, true
			}
			return models.WiFiAPConfig{Interface: iface}, true
		}
	}
	return models.WiFiAPConfig{}, false
}

// soleSavedAP returns the interface+config when one AP is saved.
func soleSavedAP() (string, models.WiFiAPConfig, bool) {
	var iface string
	var cfg models.WiFiAPConfig
	err := models.DB.QueryRow(
		"SELECT interface, ssid, channel, hw_mode, country_code, enabled FROM wifi_ap_config ORDER BY id LIMIT 1",
	).Scan(&iface, &cfg.SSID, &cfg.Channel, &cfg.HwMode, &cfg.CountryCode, &cfg.Enabled)
	if err != nil {
		return "", models.WiFiAPConfig{}, false
	}
	cfg.Interface = iface
	return iface, cfg, true
}

// restartHostapd restarts the hostapd service.
func restartHostapd() error {
	out, err := exec.Command("systemctl", "restart", "hostapd").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ============================================
// WIFI AP GET (STATUS)
// ============================================

// WiFiAPGet returns wireless adapters, their AP capability, allowed
// channels, the saved config, and live hostapd state.
func WiFiAPGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	installed := hostapdInstalled()
	running := hostapdRunning()

	var adapters []models.WiFiAdapter
	for _, name := range listWirelessInterfaces() {
		active := false
		if out, err := exec.Command("ip", "-o", "link", "show", name).Output(); err == nil {
			active = strings.Contains(string(out), "state UP") || strings.Contains(string(out), "UP>")
		}
		supportsAP := adapterSupportsAP(name)
		channels := wifiChannelsFor(name)
		if channels == nil {
			channels = []models.WiFiChannel{}
		}
		adapters = append(adapters, models.WiFiAdapter{
			Name:       name,
			SupportsAP: supportsAP,
			Active:     active,
			Channels:   channels,
		})
	}
	if adapters == nil {
		adapters = []models.WiFiAdapter{}
	}

	// Saved configs for all interfaces.
	configs := map[string]models.WiFiAPConfig{}
	rows, err := models.DB.Query("SELECT interface, ssid, channel, hw_mode, country_code, enabled FROM wifi_ap_config")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cfg models.WiFiAPConfig
			if err := rows.Scan(&cfg.Interface, &cfg.SSID, &cfg.Channel, &cfg.HwMode, &cfg.CountryCode, &cfg.Enabled); err == nil {
				configs[cfg.Interface] = cfg
			}
		}
	}

	var runningIface string
	stations := 0
	if running {
		// Which interface is hostapd serving right now?
		if cfg, ok := currentHostapdIface(); ok {
			runningIface = cfg.Interface
			stations = apStationCount(cfg.Interface)
		}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"installed":         installed,
		"running":           running,
		"running_interface": runningIface,
		"stations":          stations,
		"adapters":          adapters,
		"configs":           configs,
	})
}

// ============================================
// WIFI AP SAVE
// ============================================

// WiFiAPSave validates and stores the hotspot settings (interface, SSID,
// channel), regenerates the hostapd config, and restarts hostapd when it
// is already running so changes apply immediately. Security is always
// OPEN — there is no password field by design.
func WiFiAPSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.WiFiAPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid JSON: " + err.Error()})
		return
	}

	if !hostapdInstalled() {
		sendJSON(w, http.StatusPreconditionFailed, models.APIResponse{Success: false, Message: "hostapd is not installed. Run: apt-get install -y hostapd"})
		return
	}

	if !validateIfaceName(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid interface name"})
		return
	}
	if _, err := os.Stat("/sys/class/net/" + req.Interface); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Interface does not exist: " + req.Interface})
		return
	}
	if _, ok := readWiFiAPConfig(req.Interface); !ok && !adapterSupportsAP(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{
			Success: false,
			Message: fmt.Sprintf("%s does not support AP (master) mode. A hostapd-capable adapter is required.", req.Interface),
		})
		return
	}

	ssid := strings.TrimSpace(req.SSID)
	if len(ssid) < 1 || len(ssid) > 32 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "SSID must be 1-32 characters"})
		return
	}

	channels := wifiChannelsFor(req.Interface)
	if len(channels) == 0 {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Could not read supported channels from " + req.Interface + " (is iw installed?)"})
		return
	}
	if req.Channel <= 0 || !channelSupported(channels, req.Channel) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: fmt.Sprintf("Channel %d is not supported by %s", req.Channel, req.Interface)})
		return
	}

	country := strings.ToUpper(strings.TrimSpace(req.CountryCode))
	if country == "" {
		country = "PH"
	}
	if len(country) != 2 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Country code must be 2 letters (e.g. PH)"})
		return
	}

	cfg := models.WiFiAPConfig{
		Interface:   req.Interface,
		SSID:        ssid,
		Channel:     req.Channel,
		HwMode:      hwModeForChannel(channels, req.Channel),
		CountryCode: country,
		Enabled:     true,
	}

	if err := writeHostapdConfig(cfg); err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to write hostapd config: " + err.Error()})
		return
	}

	// Persist (upsert).
	_, err := models.DB.Exec(
		`INSERT INTO wifi_ap_config (interface, ssid, channel, hw_mode, country_code, enabled)
		 VALUES ($1, $2, $3, $4, $5, TRUE)
		 ON CONFLICT (interface) DO UPDATE
		 SET ssid = $2, channel = $3, hw_mode = $4, country_code = $5, enabled = TRUE, updated_at = NOW()`,
		cfg.Interface, cfg.SSID, cfg.Channel, cfg.HwMode, cfg.CountryCode,
	)
	if err != nil {
		log.Printf("wifi: warning: failed to persist AP config: %v", err)
	}

	// Apply immediately when the AP is already running.
	restarted := false
	if hostapdRunning() {
		if err := restartHostapd(); err != nil {
			sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Saved, but failed to restart hostapd: " + err.Error()})
			return
		}
		restarted = true
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success":   true,
		"restarted": restarted,
		"config":    cfg,
	})
}

// ============================================
// WIFI AP START / STOP
// ============================================

// WiFiAPStart brings the hotspot up: validates the saved config, puts the
// interface in AP-ready state, enables the hostapd service, and starts it.
func WiFiAPStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !hostapdInstalled() {
		sendJSON(w, http.StatusPreconditionFailed, models.APIResponse{Success: false, Message: "hostapd is not installed. Run: apt-get install -y hostapd"})
		return
	}

	var req struct {
		Interface string `json:"interface"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Interface == "" {
		// Fall back to the single saved config when no interface given.
		iface, _, ok := soleSavedAP()
		if !ok {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "No interface specified and no saved hotspot config found"})
			return
		}
		req.Interface = iface
	}

	if !validateIfaceName(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid interface name"})
		return
	}
	cfg, ok := readWiFiAPConfig(req.Interface)
	if !ok {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "No saved config for " + req.Interface + ". Save the hotspot settings first."})
		return
	}
	if _, err := os.Stat("/sys/class/net/" + req.Interface); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Interface does not exist: " + req.Interface})
		return
	}

	// Safety refusal — same style as bridge/portal provisioning: a hotspot
	// interface must be a router port, never a bridge member.
	if master := interfaceHasMaster(req.Interface); master != "" {
		sendJSON(w, http.StatusConflict, models.APIResponse{
			Success: false,
			Message: fmt.Sprintf("%s is enslaved to %s. Remove it from the bridge first.", req.Interface, master),
		})
		return
	}

	// Ensure the generated config matches the saved one.
	if err := writeHostapdConfig(cfg); err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to write hostapd config: " + err.Error()})
		return
	}

	// Bring the interface up before hostapd claims it.
	exec.Command("ip", "link", "set", req.Interface, "up").Run()

	// Enable at boot (stock unit reads DAEMON_CONF from /etc/default/hostapd).
	exec.Command("systemctl", "unmask", "hostapd").Run()
	exec.Command("systemctl", "enable", "hostapd").Run()

	if out, err := exec.Command("systemctl", "restart", "hostapd").CombinedOutput(); err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "Failed to start hostapd: " + strings.TrimSpace(string(out)),
		})
		return
	}

	// Persist the enabled flag (best-effort).
	models.DB.Exec("UPDATE wifi_ap_config SET enabled = TRUE, updated_at = NOW() WHERE interface = $1", req.Interface)

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"ssid":    cfg.SSID,
		"channel": cfg.Channel,
	})
}

// WiFiAPStop stops the hotspot (the config is kept for later restart).
func WiFiAPStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if out, err := exec.Command("systemctl", "stop", "hostapd").CombinedOutput(); err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to stop hostapd: " + strings.TrimSpace(string(out))})
		return
	}

	// Mark all saved configs disabled (single-AP system).
	models.DB.Exec("UPDATE wifi_ap_config SET enabled = FALSE, updated_at = NOW()")

	sendJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}
