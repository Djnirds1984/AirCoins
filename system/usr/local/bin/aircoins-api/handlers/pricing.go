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

// PRICING MODEL
// ------------
// 1 peso coin = 1 pulse from the coin acceptor. A pricing row maps the
// inserted peso amount (= number of pulses) to minutes of internet.
// There is no rate-per-minute: minutes always come from the table, and
// the table starts blank (the operator adds every tier manually).

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
	// ?coin=N (alias ?amount=N) resolves a single inserted peso amount to
	// minutes — used by the session manager and the captive portal.
	amountStr := r.URL.Query().Get("coin")
	if amountStr == "" {
		amountStr = r.URL.Query().Get("amount")
	}
	if amountStr != "" {
		h.quotePricing(w, amountStr)
		return
	}

	includeInactive := r.URL.Query().Get("include_inactive") == "true"

	query := `
		SELECT id, coin_value, minutes, active, pausable, expiration_hours, created_at, updated_at
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

	// Empty (not null) is the normal state of a freshly installed device
	pricing := []models.Pricing{}
	for rows.Next() {
		var p models.Pricing
		if err := rows.Scan(&p.ID, &p.CoinValue, &p.Minutes, &p.Active, &p.Pausable, &p.ExpirationHours, &p.CreatedAt, &p.UpdatedAt); err != nil {
			log.Printf("Error scanning pricing: %v", err)
			continue
		}
		pricing = append(pricing, p)
	}

	sendJSON(w, http.StatusOK, models.APIResponse{Success: true, Data: pricing})
}

// quotePricing answers "how many minutes does P<amount> buy?"
func (h *PricingHandler) quotePricing(w http.ResponseWriter, amountStr string) {
	amount, err := strconv.Atoi(amountStr)
	if err != nil || amount <= 0 {
		sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid coin amount"})
		return
	}

	match, err := MinutesForAmount(h.DB, amount)
	if err != nil {
		log.Printf("Error resolving pricing for P%d: %v", amount, err)
		sendJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Failed to resolve pricing"})
		return
	}
	minutes := match.Minutes

	if minutes == 0 {
		sendJSON(w, http.StatusOK, models.APIResponse{
			Success: false,
			Message: "No pricing tier configured for P" + strconv.Itoa(amount),
			Data: map[string]interface{}{
				"coin_value": amount,
				"minutes":    0,
				"seconds":    0,
				"matched":    0,
			},
		})
		return
	}

	sendJSON(w, http.StatusOK, models.APIResponse{
		Success: true,
		Data: map[string]interface{}{
			"coin_value": amount,
			"minutes":    minutes,
			"seconds":    minutes * 60,
			"matched":    match.MatchedCoin,
			// The tier the amount resolved to and its own listed duration, so a
			// caller can show "P7 -> charged at the P5 rate (2h)".
			"tier_coin":    match.MatchedCoin,
			"tier_minutes": match.TierMinutes,
		},
	})
}

// PricingMatch is the resolved pricing row for an inserted amount.
//
// Minutes is the time CREDITED for the resolved amount. For an amount that does
// not match a tier exactly it is pro-rated against that tier:
// amount * TierMinutes / MatchedCoin (e.g. P7 with a P5 = 2h tier credits
// 7 * 120 / 5 = 168 minutes). TierMinutes is the tier's own listed duration —
// exactly what the admin Pricing page shows for that coin.
type PricingMatch struct {
	Minutes         int
	MatchedCoin     int
	TierMinutes     int
	Pausable        bool
	ExpirationHours int
}

// pricingTier is one usable row of the pricing table.
type pricingTier struct {
	CoinValue       int
	Minutes         int
	Pausable        bool
	ExpirationHours int
}

// loadActiveTiers loads every usable active tier, cheapest first, so callers
// that must price several amounts (every coin pulse of an armed window) can do
// it in Go from one query instead of one query per pulse.
func loadActiveTiers(db *sql.DB) ([]pricingTier, error) {
	rows, err := db.Query(`
		SELECT coin_value, minutes, pausable, COALESCE(expiration_hours, 0)
		FROM pricing
		WHERE active = true AND coin_value > 0 AND minutes > 0
		ORDER BY coin_value ASC, minutes DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tiers := []pricingTier{}
	for rows.Next() {
		var t pricingTier
		if err := rows.Scan(&t.CoinValue, &t.Minutes, &t.Pausable, &t.ExpirationHours); err != nil {
			return nil, err
		}
		// Several active rows can share a coin_value on hand-edited databases
		// (the schema declares it UNIQUE): the ordering above puts the longest
		// duration / newest row first, so keep only that winner.
		if n := len(tiers); n > 0 && tiers[n-1].CoinValue == t.CoinValue {
			continue
		}
		tiers = append(tiers, t)
	}
	return tiers, rows.Err()
}

// resolveTier picks the rate that applies to an inserted amount and returns the
// PricingMatch plus the minutes credited for it. See MinutesForAmount for the
// resolution order — this is the single source of truth shared by the session
// credit, the portal's coin-window summary and GET /api/pricing.
func resolveTier(tiers []pricingTier, amount int) (PricingMatch, int) {
	if amount <= 0 || len(tiers) == 0 {
		return PricingMatch{}, 0
	}

	chosen := -1
	for i, t := range tiers {
		if t.CoinValue > amount {
			break
		}
		chosen = i
	}
	if chosen < 0 {
		// The amount is below every configured tier: still credit it at the
		// nearest (lowest) configured rate instead of 0 minutes, so a coin the
		// table does not list keeps working instead of eating the money.
		chosen = 0
	}

	t := tiers[chosen]
	credited := amount * t.Minutes / t.CoinValue
	return PricingMatch{
		Minutes:         credited,
		MatchedCoin:     t.CoinValue,
		TierMinutes:     t.Minutes,
		Pausable:        t.Pausable,
		ExpirationHours: t.ExpirationHours,
	}, credited
}

// MinutesForAmount resolves an inserted peso amount (= accumulated pulses) to
// ONE active pricing tier and returns the minutes credited for that amount.
//
// The amount is priced as a whole, never pulse by pulse: a P5 coin on a
// pulse-train coin acceptor arrives as 5 x P1 pulses, and pricing each pulse on
// its own credited 5 x the P1 rate (e.g. 5 x 15m = 1h15m for a P5 coin) while
// making the P5 / P10 / P50 tiers unreachable. Resolving the accumulated total
// makes the configured denomination rate take effect as soon as the inserted
// total reaches it.
//
// Resolution order:
//  1. the exact active tier for that amount
//  2. otherwise the highest active tier at or below the amount — the last rate
//     that applies (e.g. P7 with tiers 1/5/10 uses the P5 tier)
//  3. otherwise (amount below every tier) the lowest active tier
//
// The credited minutes are pro-rated against the chosen tier
// (amount * tier.Minutes / tier.CoinValue): an exact match credits exactly the
// listed duration, an amount between tiers credits proportionally, and a
// consumable/pausable rule always comes from that same tier.
//
// Returns Minutes = 0 when the pricing table has nothing usable — callers must
// treat that as "no time credited" and surface it to the operator.
func MinutesForAmount(db *sql.DB, amount int) (PricingMatch, error) {
	tiers, err := loadActiveTiers(db)
	if err != nil {
		return PricingMatch{}, err
	}
	match, _ := resolveTier(tiers, amount)
	return match, nil
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
		if p.ExpirationHours < 0 {
			sendJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Expiration hours must be 0 or greater"})
			return
		}

		active := true
		if p.Active != nil {
			active = *p.Active
		}
		// Default: pausable = true (a consumable rate has to be explicitly
		// created with pausable=false by the operator).
		pausable := true
		if p.Pausable != nil {
			pausable = *p.Pausable
		}

		_, err := h.DB.Exec(`
			INSERT INTO pricing (coin_value, minutes, active, pausable, expiration_hours, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (coin_value) DO UPDATE
			SET minutes = $2, active = $3, pausable = $4, expiration_hours = $5, updated_at = NOW()
		`, p.CoinValue, p.Minutes, active, pausable, p.ExpirationHours)
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
