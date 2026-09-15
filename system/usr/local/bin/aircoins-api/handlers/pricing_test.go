package handlers

import "testing"

// The tiers the operator runs in the field: P1 = 15m, P5 = 2h, P10 = 5h,
// P50 = 3 days (consumable).
var testTiers = []pricingTier{
	{CoinValue: 1, Minutes: 15, Pausable: true, ExpirationHours: 72},
	{CoinValue: 5, Minutes: 120, Pausable: true, ExpirationHours: 72},
	{CoinValue: 10, Minutes: 300, Pausable: true, ExpirationHours: 72},
	{CoinValue: 50, Minutes: 4320, Pausable: false, ExpirationHours: 0},
}

// TestResolveTierTieredByAccumulatedTotal pins the "one tier for the inserted
// total" rule: a P5 coin arrives as 5 x P1 pulses from a pulse-train acceptor
// and must claim the P5 tier (2h), not 5 x the P1 rate (75m = 1h15m).
func TestResolveTierTieredByAccumulatedTotal(t *testing.T) {
	cases := []struct {
		name         string
		tiers        []pricingTier
		amount       int
		wantMinutes  int
		wantTier     int
		wantPausable bool
	}{
		{"P1 exact tier", testTiers, 1, 15, 1, true},
		{"P5 coin paid as 5 x P1 pulses", testTiers, 5, 120, 5, true},
		{"P10 coin", testTiers, 10, 300, 10, true},
		{"P50 coin is consumable", testTiers, 50, 4320, 50, false},

		// Amounts the table does not list use the last rate that applies
		// (highest tier at or below the amount), pro-rated.
		{"P7 uses the P5 rate pro-rated", testTiers, 7, 168, 5, true},
		{"P20 uses the P10 rate pro-rated", testTiers, 20, 600, 10, true},
		{"P100 uses the P50 rate pro-rated", testTiers, 100, 8640, 50, false},

		// Below every configured tier: the lowest (last) configured rate.
		{"P1 with only a P5 tier falls back to it", []pricingTier{
			{CoinValue: 5, Minutes: 120, Pausable: true},
		}, 1, 24, 5, true},

		// Nothing configured, or nothing inserted: no time credited.
		{"no tiers configured", nil, 5, 0, 0, false},
		{"zero amount", testTiers, 0, 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match, credited := resolveTier(tc.tiers, tc.amount)
			if credited != tc.wantMinutes || match.Minutes != tc.wantMinutes {
				t.Fatalf("amount %d: credited=%d match.Minutes=%d, want %d",
					tc.amount, credited, match.Minutes, tc.wantMinutes)
			}
			if match.MatchedCoin != tc.wantTier {
				t.Fatalf("amount %d: matched coin=P%d, want P%d",
					tc.amount, match.MatchedCoin, tc.wantTier)
			}
			if match.Pausable != tc.wantPausable {
				t.Fatalf("amount %d: pausable=%v, want %v",
					tc.amount, match.Pausable, tc.wantPausable)
			}
		})
	}
}

// TestResolveTierMarginalsSumToWindowTotal guards the portal's coin-window
// summary: the per-coin lines are marginal credits, so they must add up to the
// window total the session credits.
func TestResolveTierMarginalsSumToWindowTotal(t *testing.T) {
	// Five P1 pulses (= a P5 coin).
	pulses := []int{1, 1, 1, 1, 1}

	running := 0
	sum := 0
	for _, p := range pulses {
		_, before := resolveTier(testTiers, running)
		running += p
		_, after := resolveTier(testTiers, running)
		sum += after - before
	}

	_, total := resolveTier(testTiers, running)
	if sum != total {
		t.Fatalf("marginal lines sum to %d min but the window total is %d min", sum, total)
	}
	if total != 120 {
		t.Fatalf("5 x P1 pulses credited %d min, want 120 min (the P5 tier)", total)
	}
}
