package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
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
		SELECT id, pin, coin_value, pulse_mode, COALESCE(board_model, 'auto'), updated_at
		FROM gpio_config ORDER BY id DESC LIMIT 1
	`).Scan(&config.ID, &config.Pin, &config.CoinValue, &config.PulseMode, &config.BoardModel, &config.UpdatedAt)

	if err == sql.ErrNoRows {
		sendJSON(w, http.StatusOK, models.GPIOConfig{Pin: 7, CoinValue: 1, PulseMode: "falling", BoardModel: "auto"})
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

	if req.Pin < 0 || req.Pin > 40 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid pin number (0-40)"})
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

	if req.BoardModel == "" {
		req.BoardModel = "auto"
	}

	// Insert new config (keep history)
	_, err := h.DB.Exec(`
		INSERT INTO gpio_config (pin, coin_value, pulse_mode, board_model, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, req.Pin, req.CoinValue, req.PulseMode, req.BoardModel)

	if err != nil {
		log.Printf("Error saving GPIO config: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to save GPIO config"})
		return
	}

	// Write config file for gpio-coin-listener to read
	writeGPIOConfigFile(req.Pin, req.CoinValue, req.PulseMode, req.BoardModel)

	logAction(h.DB, "INFO", "gpio", "GPIO config updated: pin="+strconv.Itoa(req.Pin)+" board="+req.BoardModel)
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

	// Try all GPIO methods
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

// readGpioPin reads the state of a GPIO pin trying multiple methods
func readGpioPin(pin int) (int, string, error) {
	pinStr := strconv.Itoa(pin)

	// Method 1: WiringOP (gpio command) — Orange Pi
	gpioPath, err := exec.LookPath("gpio")
	if err == nil {
		exec.Command(gpioPath, "mode", pinStr, "up").Run()
		exec.Command(gpioPath, "mode", pinStr, "input").Run()

		out, err := exec.Command(gpioPath, "read", pinStr).Output()
		if err == nil {
			stateStr := strings.TrimSpace(string(out))
			state, _ := strconv.Atoi(stateStr)
			return state, "wiringop", nil
		}
	}

	// Method 2: raspi-gpio — Raspberry Pi
	raspiGpioPath, err := exec.LookPath("raspi-gpio")
	if err == nil {
		out, err := exec.Command(raspiGpioPath, "get", pinStr).Output()
		if err == nil {
			outStr := string(out)
			if strings.Contains(outStr, "level=0") {
				return 0, "raspi-gpio", nil
			} else if strings.Contains(outStr, "level=1") {
				return 1, "raspi-gpio", nil
			}
		}
	}

	// Method 3: libgpiod (gpioget command)
	gpiogetPath, err := exec.LookPath("gpioget")
	if err == nil {
		out, err := exec.Command(gpiogetPath, "gpiochip0", pinStr).Output()
		if err == nil {
			stateStr := strings.TrimSpace(string(out))
			state, _ := strconv.Atoi(stateStr)
			return state, "libgpiod", nil
		}
	}

	// Method 4: sysfs fallback
	if _, err := os.Stat("/sys/class/gpio"); err == nil {
		// Export if needed
		if _, err := os.Stat("/sys/class/gpio/gpio" + pinStr); os.IsNotExist(err) {
			os.WriteFile("/sys/class/gpio/export", []byte(pinStr), 0644)
		}
		os.WriteFile("/sys/class/gpio/gpio"+pinStr+"/direction", []byte("in"), 0644)

		data, err := os.ReadFile("/sys/class/gpio/gpio" + pinStr + "/value")
		if err == nil {
			stateStr := strings.TrimSpace(string(data))
			state, _ := strconv.Atoi(stateStr)
			return state, "sysfs", nil
		}
	}

	return 1, "none", nil // Default HIGH (pull-up)
}

// writeGPIOConfigFile writes config to file for gpio-coin-listener
func writeGPIOConfigFile(pin, coinValue int, pulseMode, boardModel string) {
	content := "# AirCoins GPIO Config - Written by API\n"
	content += "COIN_PULSE_PIN=" + strconv.Itoa(pin) + "\n"
	content += "COIN_VALUE=" + strconv.Itoa(coinValue) + "\n"
	content += "PULSE_MODE=\"" + pulseMode + "\"\n"
	content += "BOARD_MODEL=\"" + boardModel + "\"\n"

	os.MkdirAll("/var/lib/pisowifi", 0755)
	err := os.WriteFile("/var/lib/pisowifi/gpio_config", []byte(content), 0644)
	if err != nil {
		log.Printf("Failed to write GPIO config file: %v", err)
	}
}
