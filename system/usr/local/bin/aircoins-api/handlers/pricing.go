package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type PricingHandler struct {
	DB *sql.DB
}

// Handle handles pricing GET, POST, and DELETE
func (h *PricingHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// Check if this is a DELETE request with an ID in the path
	// Path format: /api/pricing/{id}
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/pricing/") && path != "/api/pricing/" {
		idStr := strings.TrimPrefix(path, "/api/pricing/")
		if idStr != "" {
			if r.Method == http.MethodDelete {
				h.deletePricing(w, r, idStr)
				return
			}
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		h.getPricing(w, r)
	case http.MethodPost:
		h.updatePricing(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *PricingHandler) getPricing(w http.ResponseWriter, r *http.Request) {
	includeInactive := r.URL.Query().Get("include_inactive") == "true"

	query := `
		SELECT id, coin_value, minutes, active, created_at, updated_at
		FROM pricing
	`
	if !includeInactive {
		query += ` WHERE active = true`
	}
	query += ` ORDER BY coin_value ASC`

	rows, err := h.DB.Query(query)
	if err != nil {
		log.Printf("Error fetching pricing: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to fetch pricing"})
		return
	}
	defer rows.Close()

	var pricing []models.Pricing
	for rows.Next() {
		var p models.Pricing
		if err := rows.Scan(&p.ID, &p.CoinValue, &p.Minutes, &p.Active, &p.CreatedAt, &p.UpdatedAt); err != nil {
			log.Printf("Error scanning pricing: %v", err)
			continue
		}
		pricing = append(pricing, p)
	}

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: pricing})
}

func (h *PricingHandler) updatePricing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pricing []models.PricingRequest `json:"pricing"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body"})
		return
	}

	for _, p := range req.Pricing {
		if p.CoinValue <= 0 || p.Minutes <= 0 {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Coin value and minutes must be positive"})
			return
		}

		active := true
		if p.Active != nil {
			active = *p.Active
		}

		_, err := h.DB.Exec(`
			INSERT INTO pricing (coin_value, minutes, active, updated_at)
			VALUES ($1, $2, $3, NOW())
			ON CONFLICT (coin_value) DO UPDATE 
			SET minutes = $2, active = $3, updated_at = NOW()
		`, p.CoinValue, p.Minutes, active)
		if err != nil {
			log.Printf("Error updating pricing: %v", err)
		}
	}

	logAction(h.DB, "INFO", "pricing", "Pricing updated")
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Pricing updated"})
}

func (h *PricingHandler) deletePricing(w http.ResponseWriter, r *http.Request, idStr string) {
	id, err := strconv.Atoi(idStr)
	if err != nil {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid pricing ID"})
		return
	}

	result, err := h.DB.Exec(`UPDATE pricing SET active = false, updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		log.Printf("Error deleting pricing: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to delete pricing"})
		return
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		log.Printf("Error checking rows affected: %v", err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to delete pricing"})
		return
	}

	if rowsAffected == 0 {
		sendJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "Pricing tier not found"})
		return
	}

	logAction(h.DB, "INFO", "pricing", "Pricing tier deactivated: ID "+idStr)
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Pricing tier deactivated"})
}
