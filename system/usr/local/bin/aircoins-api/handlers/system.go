package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"log"
	"net/http"
	"os/exec"
)

type SystemHandler struct {
	DB *sql.DB
}

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
			"database":  dbStatus,
			"lighttpd":  lighttpdRunning,
		},
		"system_online": lighttpdRunning,
	})
}

// Logs returns recent system logs
func (h *SystemHandler) Logs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := h.DB.Query(`
		SELECT id, level, component, message, created_at
		FROM system_logs
		ORDER BY created_at DESC
		LIMIT 50
	`)
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

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: logs})
}

// checkServiceRunning checks if a systemd service is running
func checkServiceRunning(service string) bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", service)
	err := cmd.Run()
	return err == nil
}
