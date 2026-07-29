package handlers

import (
	"aircoins-api/models"
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const vlanConfigPath = "/etc/pisowifi/vlans.conf"

// ============================================
// VLAN LIST
// ============================================

// VLANList returns all active VLANs merged with saved config descriptions.
func VLANList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get active VLANs from ip command
	out, err := exec.Command("ip", "-d", "link", "show", "type", "vlan").Output()
	if err != nil {
		log.Printf("Failed to list VLANs: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "Failed to list VLANs: " + err.Error(),
		})
		return
	}

	// Parse ip -d link show type vlan output
	// Block format:
	//   N: name@parent: <FLAGS> ...
	//       ...
	//       vlan id X ...
	vlanRe := regexp.MustCompile(`^\d+:\s+(\S+)@(\S+):`)
	idRe := regexp.MustCompile(`vlan\s+id\s+(\d+)`)

	var activeVLANs []models.VLANInfo
	lines := strings.Split(string(out), "\n")
	var currentIface, currentParent string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := vlanRe.FindStringSubmatch(trimmed); m != nil {
			currentIface = m[1]
			currentParent = m[2]
			continue
		}

		if currentIface == "" {
			continue
		}

		if m := idRe.FindStringSubmatch(trimmed); m != nil {
			vid, _ := strconv.Atoi(m[1])

			// Get IP for this VLAN interface
			ip := getVLANIP(currentIface)

			activeVLANs = append(activeVLANs, models.VLANInfo{
				Interface: currentParent,
				VLANID:    vid,
				Name:      currentIface,
				IP:        ip,
				Active:    true,
			})

			currentIface = ""
			currentParent = ""
		}
	}

	// Merge with saved config (descriptions + portal flag)
	cfgEntries := readVLANConfig()
	cfgMap := make(map[string]*vlanConfigEntry)
	for i := range cfgEntries {
		key := fmt.Sprintf("%s.%d", cfgEntries[i].Interface, cfgEntries[i].VLANID)
		cfgMap[key] = &cfgEntries[i]
	}

	for i := range activeVLANs {
		key := fmt.Sprintf("%s.%d", activeVLANs[i].Interface, activeVLANs[i].VLANID)
		if cfg, ok := cfgMap[key]; ok {
			activeVLANs[i].Description = cfg.Description
			activeVLANs[i].IsPortal = cfg.IsPortal
			if cfg.IP != "" && activeVLANs[i].IP == "" {
				activeVLANs[i].IP = cfg.IP
			}
		}
	}

	if activeVLANs == nil {
		activeVLANs = []models.VLANInfo{}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"vlans": activeVLANs})
}

// ============================================
// VLAN CREATE
// ============================================

// VLANCreate creates a new VLAN interface and persists the config.
func VLANCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.VLANRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid JSON: " + err.Error()})
		return
	}

	// Validate VLAN ID
	if req.VLANID < 1 || req.VLANID > 4094 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "VLAN ID must be between 1 and 4094"})
		return
	}

	// Validate interface exists and is not lo or a path-traversal attempt
	if !validateIfaceName(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid interface name"})
		return
	}
	if _, err := os.Stat("/sys/class/net/" + req.Interface); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Interface does not exist: " + req.Interface})
		return
	}

	// Validate IP
	if net.ParseIP(req.IP) == nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid IP address"})
		return
	}

	// Validate netmask and convert to CIDR prefix length
	cidr, err := netmaskToCIDR(req.Netmask)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: err.Error()})
		return
	}

	vlanName := fmt.Sprintf("%s.%d", req.Interface, req.VLANID)
	ipCIDR := fmt.Sprintf("%s/%d", req.IP, cidr)

	// 1. Create VLAN interface
	cmdOut, err := exec.Command("ip", "link", "add", "link", req.Interface, "name", vlanName, "type", "vlan", "id", strconv.Itoa(req.VLANID)).CombinedOutput()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to create VLAN: " + string(cmdOut)})
		return
	}

	// 2. Assign IP address
	cmdOut, err = exec.Command("ip", "addr", "add", ipCIDR, "dev", vlanName).CombinedOutput()
	if err != nil {
		exec.Command("ip", "link", "delete", vlanName).Run()
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to assign IP: " + string(cmdOut)})
		return
	}

	// 3. Bring interface up
	cmdOut, err = exec.Command("ip", "link", "set", vlanName, "up").CombinedOutput()
	if err != nil {
		exec.Command("ip", "link", "delete", vlanName).Run()
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to bring VLAN up: " + string(cmdOut)})
		return
	}

	// Save to database (UPSERT)
	var existing int
	err = models.DB.QueryRow(
		"SELECT id FROM vlan_config WHERE interface=$1 AND vlan_id=$2",
		req.Interface, req.VLANID,
	).Scan(&existing)

	if err == sql.ErrNoRows {
		_, err = models.DB.Exec(
			"INSERT INTO vlan_config (interface, vlan_id, ip_address, description, is_portal) VALUES ($1,$2,$3,$4,$5)",
			req.Interface, req.VLANID, ipCIDR, req.Description, req.IsPortal,
		)
	} else if err == nil {
		_, err = models.DB.Exec(
			"UPDATE vlan_config SET ip_address=$3, description=$4, is_portal=$5 WHERE interface=$1 AND vlan_id=$2",
			req.Interface, req.VLANID, ipCIDR, req.Description, req.IsPortal,
		)
	}
	if err != nil {
		log.Printf("Warning: failed to persist VLAN to DB: %v", err)
	}

	// Save to config file
	writeVLANConfigEntry(vlanConfigEntry{
		Interface:   req.Interface,
		VLANID:      req.VLANID,
		IP:          ipCIDR,
		Description: req.Description,
		IsPortal:    req.IsPortal,
	})

	vlan := models.VLANInfo{
		Interface:   req.Interface,
		VLANID:      req.VLANID,
		Name:        vlanName,
		IP:          ipCIDR,
		Description: req.Description,
		IsPortal:    req.IsPortal,
		Active:      true,
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"success": true, "vlan": vlan})
}

// ============================================
// VLAN DELETE
// ============================================

// VLANDelete removes a VLAN interface and its persisted config.
func VLANDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Interface string `json:"interface"`
		VLANID    int    `json:"vlan_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid JSON: " + err.Error()})
		return
	}

	if req.VLANID < 1 || req.VLANID > 4094 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "VLAN ID must be between 1 and 4094"})
		return
	}
	if !validateIfaceName(req.Interface) {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid interface name"})
		return
	}

	vlanName := fmt.Sprintf("%s.%d", req.Interface, req.VLANID)

	// Delete the interface
	cmdOut, err := exec.Command("ip", "link", "delete", vlanName).CombinedOutput()
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to delete VLAN: " + string(cmdOut)})
		return
	}

	// Remove from config file
	removeVLANConfigEntry(req.Interface, req.VLANID)

	// Delete from database
	if _, err := models.DB.Exec("DELETE FROM vlan_config WHERE interface=$1 AND vlan_id=$2", req.Interface, req.VLANID); err != nil {
		log.Printf("Warning: failed to remove VLAN from DB: %v", err)
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

// ============================================
// VLAN INTERFACES
// ============================================

// VLANInterfaces returns physical network interfaces with their IPs and state.
func VLANInterfaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to read interfaces: " + err.Error()})
		return
	}

	var ifaces []models.InterfaceInfo
	for _, entry := range entries {
		name := entry.Name()
		if name == "lo" {
			continue
		}
		// Only include physical interfaces (eth*, wlan*, en*)
		if !(strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "wlan") || strings.HasPrefix(name, "en")) {
			continue
		}

		ip := getInterfaceIP(name)
		state := getInterfaceState(name)

		ifaces = append(ifaces, models.InterfaceInfo{
			Name:  name,
			IP:    ip,
			State: state,
		})
	}

	if ifaces == nil {
		ifaces = []models.InterfaceInfo{}
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{"interfaces": ifaces})
}

// ============================================
// IP HELPERS
// ============================================

// getVLANIP returns the first IPv4 address (with CIDR) for a VLAN interface.
func getVLANIP(iface string) string {
	out, err := exec.Command("ip", "-4", "addr", "show", iface).Output()
	if err != nil {
		return ""
	}
	return parseIPFromOutput(string(out))
}

// getInterfaceIP returns the first IPv4 address (with CIDR) for a physical interface.
func getInterfaceIP(iface string) string {
	out, err := exec.Command("ip", "-4", "addr", "show", iface).Output()
	if err != nil {
		return ""
	}
	return parseIPFromOutput(string(out))
}

// parseIPFromOutput extracts the first inet address from `ip addr show` output.
func parseIPFromOutput(out string) string {
	re := regexp.MustCompile(`inet\s+(\S+)`)
	for _, line := range strings.Split(out, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// getInterfaceState reads the operstate of a network interface from sysfs.
func getInterfaceState(iface string) string {
	data, err := os.ReadFile("/sys/class/net/" + iface + "/operstate")
	if err != nil {
		return "unknown"
	}
	state := strings.TrimSpace(string(data))
	if state == "" {
		return "unknown"
	}
	return state
}

// ============================================
// VLAN CONFIG FILE HELPERS
// ============================================

// vlanConfigEntry represents one line in /etc/pisowifi/vlans.conf.
type vlanConfigEntry struct {
	Interface   string
	VLANID      int
	IP          string
	Description string
	IsPortal    bool
}

// readVLANConfig reads all entries from /etc/pisowifi/vlans.conf.
// Format: interface vlan_id ip/cidr description [portal]
// Example: eth0 100 192.168.100.1/24 Portal VLAN portal
func readVLANConfig() []vlanConfigEntry {
	f, err := os.Open(vlanConfigPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []vlanConfigEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split into at most 5 fields; description may contain spaces only if
		// it is the 4th token and everything up to the optional "portal" flag.
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}

		vid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		entry := vlanConfigEntry{
			Interface: parts[0],
			VLANID:    vid,
			IP:        parts[2],
		}

		// Remaining tokens: description words + optional "portal" flag
		if len(parts) > 3 {
			rest := parts[3:]
			if rest[len(rest)-1] == "portal" {
				entry.IsPortal = true
				rest = rest[:len(rest)-1]
			}
			entry.Description = strings.Join(rest, " ")
		}

		entries = append(entries, entry)
	}

	return entries
}

// writeVLANConfig rewrites the entire vlans.conf from the given entries.
func writeVLANConfig(entries []vlanConfigEntry) {
	if err := os.MkdirAll(filepath.Dir(vlanConfigPath), 0755); err != nil {
		log.Printf("Failed to create config dir: %v", err)
		return
	}

	f, err := os.OpenFile(vlanConfigPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Printf("Failed to open VLAN config for writing: %v", err)
		return
	}
	defer f.Close()

	for _, e := range entries {
		line := fmt.Sprintf("%s %d %s %s", e.Interface, e.VLANID, e.IP, e.Description)
		if e.IsPortal {
			line += " portal"
		}
		fmt.Fprintln(f, line)
	}
}

// writeVLANConfigEntry appends or updates a single entry, then rewrites the file.
func writeVLANConfigEntry(entry vlanConfigEntry) {
	entries := readVLANConfig()

	found := false
	for i := range entries {
		if entries[i].Interface == entry.Interface && entries[i].VLANID == entry.VLANID {
			entries[i] = entry
			found = true
			break
		}
	}
	if !found {
		entries = append(entries, entry)
	}

	writeVLANConfig(entries)
}

// removeVLANConfigEntry deletes an entry and rewrites the file.
func removeVLANConfigEntry(iface string, vlanID int) {
	entries := readVLANConfig()

	var filtered []vlanConfigEntry
	for _, e := range entries {
		if e.Interface == iface && e.VLANID == vlanID {
			continue
		}
		filtered = append(filtered, e)
	}

	writeVLANConfig(filtered)
}

// ============================================
// NETMASK HELPERS
// ============================================

// validateIfaceName rejects interface names that could cause path traversal or
// shell injection. Only alphanumeric chars, dots, hyphens, and underscores are
// allowed; the name must not start with a dot or contain "..".
func validateIfaceName(name string) bool {
	if name == "" || name == "lo" {
		return false
	}
	if strings.Contains(name, "..") || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// netmaskToCIDR converts a dotted-decimal netmask to a CIDR prefix length.
// e.g. "255.255.255.0" → 24
func netmaskToCIDR(mask string) (int, error) {
	ip := net.ParseIP(mask)
	if ip == nil {
		return 0, fmt.Errorf("invalid netmask: %s", mask)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, fmt.Errorf("netmask must be IPv4")
	}

	ones, bits := net.IPMask(ip4).Size()
	if bits == 0 {
		return 0, fmt.Errorf("invalid subnet mask: %s", mask)
	}

	return ones, nil
}
