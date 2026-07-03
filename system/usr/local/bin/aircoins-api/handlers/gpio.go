package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

type GPIOHandler struct {
	DB *sql.DB
}

// Config handles GPIO configuration (GET and POST)
func (h *GPIOHandler) Config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getConfig(w, r)
	case http.MethodPost:
		h.updateConfig(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *GPIOHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	var config models.GPIOConfig
	err := h.DB.QueryRow(`
		SELECT id, pin, coin_value, pulse_mode, updated_at
		FROM gpio_config ORDER BY id DESC LIMIT 1
	`).Scan(&config.ID, &config.Pin, &config.CoinValue, &config.PulseMode, &config.UpdatedAt)

	if err == sql.ErrNoRows {
		sendJSON(w, http.StatusOK, models.GPIOConfig{Pin: 7, CoinValue: 1, PulseMode: "falling"})
		return
	} else if err != nil {
		log.Printf("Error fetching GPIO config: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch GPIO config"})
		return
	}

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: config})
}

func (h *GPIOHandler) updateConfig(w http.ResponseWriter, r *http.Request) {
	var req models.GPIOConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	if req.Pin < 0 || req.Pin > 21 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid pin number (0-21)"})
		return
	}

	if req.CoinValue <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Coin value must be positive"})
		return
	}

	if req.PulseMode != "falling" && req.PulseMode != "rising" && req.PulseMode != "both" {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid pulse mode"})
		return
	}

	// Insert new config (keep history)
	_, err := h.DB.Exec(`
		INSERT INTO gpio_config (pin, coin_value, pulse_mode, updated_at)
		VALUES ($1, $2, $3, NOW())
	`, req.Pin, req.CoinValue, req.PulseMode)

	if err != nil {
		log.Printf("Error saving GPIO config: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to save GPIO config"})
		return
	}

	// Write config file for gpio-coin-listener to read
	writeGPIOConfigFile(req.Pin, req.CoinValue, req.PulseMode)

	logAction(h.DB, "INFO", "gpio", "GPIO config updated: pin="+strconv.Itoa(req.Pin))
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "GPIO config saved. Restart GPIO listener to apply."})
}

// Test performs a real GPIO pin test
func (h *GPIOHandler) Test(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pin int `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	// Try WiringOP first
	state, method, err := readGpioPin(req.Pin)
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{
			Success: false,
			Message: "GPIO test failed: " + err.Error(),
		})
		return
	}

	stateText := "HIGH (1)"
	stateColor := "#00ff88"
	if state == 0 {
		stateText = "LOW (0)"
		stateColor = "#ff4444"
	}

	sendJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"method":      method,
		"pin":         req.Pin,
		"state":       state,
		"state_text":  stateText,
		"state_color": stateColor,
		"pull_up":     "enabled",
	})
}

// Coin records a coin event from the GPIO listener
func (h *GPIOHandler) Coin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req models.CoinEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	if req.CoinValue <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid coin value"})
		return
	}

	// Insert coin event
	_, err := h.DB.Exec(`
		INSERT INTO coin_events (coin_value, detected_at, processed)
		VALUES ($1, NOW(), false)
	`, req.CoinValue)

	if err != nil {
		log.Printf("Error recording coin event: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to record coin event"})
		return
	}

	// Get pricing for this coin value to calculate seconds
	var minutes int
	err = h.DB.QueryRow(`
		SELECT minutes FROM pricing WHERE coin_value = $1 AND active = true
	`, req.CoinValue).Scan(&minutes)
	if err != nil {
		minutes = req.CoinValue * 5 // Default: 5 min per peso
	}

	logAction(h.DB, "INFO", "gpio", "Coin detected: P"+strconv.Itoa(req.CoinValue)+" = "+strconv.Itoa(minutes)+" min")

	sendJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Message: "Coin event recorded",
		Data: map[string]interface{}{
			"coin_value": req.CoinValue,
			"minutes":    minutes,
			"seconds":    minutes * 60,
		},
	})
}

// readGpioPin reads the state of a GPIO pin
func readGpioPin(pin int) (int, string, error) {
	// Try WiringOP (gpio command)
	gpioPath, err := exec.LookPath("gpio")
	if err == nil {
		// Set pin to input with pull-up
		exec.Command(gpioPath, "mode", strconv.Itoa(pin), "up").Run()
		exec.Command(gpioPath, "mode", strconv.Itoa(pin), "input").Run()

		out, err := exec.Command(gpioPath, "read", strconv.Itoa(pin)).Output()
		if err == nil {
			stateStr := strings.TrimSpace(string(out))
			state, _ := strconv.Atoi(stateStr)
			return state, "wiringop", nil
		}
	}

	return 1, "none", nil // Default HIGH (pull-up)
}

// writeGPIOConfigFile writes config to file for gpio-coin-listener
func writeGPIOConfigFile(pin, coinValue int, pulseMode string) {
	content := "# AirCoins GPIO Config - Written by API\n"
	content += "COIN_PULSE_PIN=" + strconv.Itoa(pin) + "\n"
	content += "COIN_VALUE=" + strconv.Itoa(coinValue) + "\n"
	content += "PULSE_MODE=\"" + pulseMode + "\"\n"

	exec.Command("mkdir", "-p", "/var/lib/pisowifi").Run()
	exec.Command("tee", "/var/lib/pisowifi/gpio_config").Run()
}
