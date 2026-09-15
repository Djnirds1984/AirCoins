package handlers

// dnswatch.go — DNS forwarder watcher + admin refresh endpoint.
//
// Separates WAN DNS (dynamic, via a dnsmasq forwarder on port 5353 that
// follows /etc/resolv.conf) from the hotspot's captive DNS hijack (wildcard,
// stays on the gateway IP). Authorized (paying) clients are REDIRECT'ed to
// the forwarder rather than DNAT'ed to a frozen UPSTREAM_DNS, so they keep
// real internet DNS when the box is moved to another ISP/DHCP.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// dnsWatchConfig holds the paths/commands the DNS watcher uses.
type dnsWatchConfig struct {
	captiveRulesPath string
	dnsForwarderPort string
}

var dnsWatch *dnsWatchConfig
var cachedUpstream string
var dnsWatchStarted bool

// InitDNSWatch sets the captive-rules path used by the DNS watcher.
// Called once from main.go before starting the watcher.
func InitDNSWatch(captiveRulesPath string) {
	dnsWatch = &dnsWatchConfig{
		captiveRulesPath: captiveRulesPath,
		dnsForwarderPort: "5353",
	}
}

// StartDNSWatcher ensures the DNS forwarder is running and polls for
// upstream-resolver changes so paying clients keep internet when the ISP
// or DHCP changes. Runs in a goroutine; idempotent via dnsWatchStarted.
func StartDNSWatcher() {
	if dnsWatchStarted {
		return
	}
	dnsWatchStarted = true

	log.Println("[dnswatch] starting DNS watcher (forwarder + upstream polling)")

	// One-shot at startup: ensure the forwarder + rebuild authorized-client
	// rules (heals stale DNAT rules from older versions targeting a frozen
	// UPSTREAM_DNS).
	refreshDNSNow()

	// Poll every 60s: re-ensure forwarder + detect upstream changes.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			refreshDNSNow()
		}
	}()
}

// refreshDNSNow is the single refresh path used by the watcher and the
// admin endpoint. It:
//  1. Ensures the dnsmasq forwarder on port 5353 is running.
//  2. Calls "aircoins-captive-rules refresh" to rebuild per-MAC REDIRECT
//     rules against the forwarder (heals stale DNAT rules from older
//     versions that targeted a frozen UPSTREAM_DNS).
func refreshDNSNow() {
	if dnsWatch == nil || dnsWatch.captiveRulesPath == "" {
		log.Println("[dnswatch] skip DNS forwarder (captive-rules path not set)")
		return
	}

	// 1. Ensure forwarder is present + running (piggyback on refresh).
	if err := runCaptiveRulesScript("refresh"); err != nil {
		log.Printf("[dnswatch] refresh failed: %v", err)
		return
	}

	upstream := detectUpstreamDNS()
	if upstream != cachedUpstream {
		log.Printf("[dnswatch] upstream DNS changed: %q -> %q; rules rebuilt", cachedUpstream, upstream)
		cachedUpstream = upstream
	} else {
		log.Println("[dnswatch] dns refresh OK (upstream unchanged: " + upstream + ")")
	}
}

// detectUpstreamDNS reads the current resolver from /etc/resolv.conf.
// Returns the first nameserver IP found, or "" if none.
func detectUpstreamDNS() string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "nameserver") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}

// runCaptiveRulesScript invokes aircoins-captive-rules with the given subcommand.
func runCaptiveRulesScript(subcommand string) error {
	if dnsWatch == nil || dnsWatch.captiveRulesPath == "" {
		return fmt.Errorf("captive-rules path not configured")
	}
	cmd := exec.Command(dnsWatch.captiveRulesPath, subcommand)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("aircoins-captive-rules %s: %w (%s)", subcommand, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RefreshDNSHandler is the admin endpoint that manually triggers a DNS
// forwarder refresh + rebuild of authorized-client rules.
// POST /api/admin/captive/refresh-dns
func RefreshDNSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	refreshDNSNow()
	writeJSON(w, http.StatusOK, map[string]string{
		"status":         "ok",
		"forwarder_port": dnsForwarderPort(),
		"upstream":       detectedUpstream(),
	})
}

func dnsForwarderPort() string {
	if dnsWatch == nil {
		return "5353"
	}
	return dnsWatch.dnsForwarderPort
}

func detectedUpstream() string {
	return detectUpstreamDNS()
}

// CaptiveStatusHandler returns the current DNS-forwarder state for admin
// diagnostics. GET /api/admin/captive/status
func CaptiveStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"forwarder_port":    dnsForwarderPort(),
		"upstream_dns":      detectUpstreamDNS(),
		"forwarder_running": isForwarderRunning(),
	})
}

func isForwarderRunning() bool {
	// Lightweight check: does something listen on port 5353?
	cmd := exec.Command("ss", "-ltn")
	out, err := cmd.CombinedOutput()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, ":5353") {
				return true
			}
		}
	}
	return false
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}