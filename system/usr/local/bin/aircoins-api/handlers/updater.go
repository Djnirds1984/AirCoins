package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// UpdaterHandler caches GitHub release lookups to avoid hammering the API.
type UpdaterHandler struct {
	mu       sync.RWMutex
	cachedAt time.Time
	cached   *releaseInfo
	client   *http.Client
}

type releaseInfo struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

// NewUpdaterHandler creates an UpdaterHandler with a 10-second HTTP client.
func NewUpdaterHandler() *UpdaterHandler {
	return &UpdaterHandler{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// CheckForUpdate queries the latest GitHub release and compares versions.
func (h *UpdaterHandler) CheckForUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	force := r.URL.Query().Get("force") == "true"

	// Check cache (unless forced refresh)
	h.mu.RLock()
	if !force && h.cached != nil && time.Since(h.cachedAt) < 1*time.Hour {
		release := *h.cached
		h.mu.RUnlock()

		updateAvailable := compareSemver(apiVersion, release.TagName) < 0
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"latest_version":   release.TagName,
			"update_available": updateAvailable,
			"release_url":      release.HTMLURL,
			"release_notes":    release.Body,
			"published_at":     release.PublishedAt,
			"checked_at":       time.Now().UTC().Format(time.RFC3339),
		})
		return
	}
	h.mu.RUnlock()

	// Fetch latest release from GitHub
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/Djnirds1984/AirCoins/releases/latest", nil)
	if err != nil {
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"update_available": false,
			"error":            fmt.Sprintf("Unable to check for updates: %v", err),
		})
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AirCoins-Updater")

	resp, err := h.client.Do(req)
	if err != nil {
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"update_available": false,
			"error":            fmt.Sprintf("Unable to check for updates: %v", err),
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"update_available": false,
			"error":            fmt.Sprintf("Unable to check for updates: HTTP %d: %s", resp.StatusCode, string(body)),
		})
		return
	}

	var release releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		sendJSON(w, 200, map[string]interface{}{
			"current_version":  apiVersion,
			"update_available": false,
			"error":            fmt.Sprintf("Unable to check for updates: %v", err),
		})
		return
	}

	// Update cache
	h.mu.Lock()
	h.cached = &release
	h.cachedAt = time.Now()
	h.mu.Unlock()

	updateAvailable := compareSemver(apiVersion, release.TagName) < 0

	sendJSON(w, 200, map[string]interface{}{
		"current_version":  apiVersion,
		"latest_version":   release.TagName,
		"update_available": updateAvailable,
		"release_url":      release.HTMLURL,
		"release_notes":    release.Body,
		"published_at":     release.PublishedAt,
		"checked_at":       time.Now().UTC().Format(time.RFC3339),
	})
}

// compareSemver compares two semantic version strings.
// Returns -1 if a < b, 0 if equal, +1 if a > b.
// "dev" is always considered less than any version.
func compareSemver(a, b string) int {
	if a == "dev" && b == "dev" {
		return 0
	}
	if a == "dev" {
		return -1
	}
	if b == "dev" {
		return 1
	}

	a = strings.TrimLeft(a, "vV")
	b = strings.TrimLeft(b, "vV")

	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)

	if len(aParts) < 3 || len(bParts) < 3 {
		return 0
	}

	for i := 0; i < 3; i++ {
		// Strip pre-release suffix (e.g., "0-beta" → "0")
		if idx := strings.IndexAny(aParts[i], "-+"); idx >= 0 {
			aParts[i] = aParts[i][:idx]
		}
		if idx := strings.IndexAny(bParts[i], "-+"); idx >= 0 {
			bParts[i] = bParts[i][:idx]
		}
		av, err := strconv.Atoi(aParts[i])
		if err != nil {
			return 0
		}
		bv, err := strconv.Atoi(bParts[i])
		if err != nil {
			return 0
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
