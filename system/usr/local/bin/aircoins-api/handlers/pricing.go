package handlers

import (
	"aircoins-api/models"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

type PricingHandler struct {
	DB *sql.DB
}

// Handle handles pricing GET and POST
func (h *PricingHandler) Handle(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.DB.Query(`
		SELECT id, coin_value, minutes, active, created_at, updated_at
		FROM pricing
		WHERE active = true
		ORDER BY coin_value ASC
	`)
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

		_, err := h.DB.Exec(`
			INSERT INTO pricing (coin_value, minutes, active, updated_at)
			VALUES ($1, $2, true, NOW())
			ON CONFLICT (coin_value) DO UPDATE 
			SET minutes = $2, active = true, updated_at = NOW()
		`, p.CoinValue, p.Minutes)
		if err != nil {
			log.Printf("Error updating pricing: %v", err)
		}
	}

	logAction(h.DB, "INFO", "pricing", "Pricing updated")
	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Pricing updated"})
}
