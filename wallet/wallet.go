package wallet

import (
	"encoding/json"
	"fmt"
	"time"
)

// Currency is an ISO code (usd, idr, eur). Decimal precision is handled
// per-currency in parseAmountToCents/formatCents, not as one constant
// for everything — see the README for why.
type Currency string

const (
	USD Currency = "usd"
	IDR Currency = "idr"
	EUR Currency = "eur"
)

var Currencies = []Currency{USD, IDR, EUR}

// decimalPlaces is how many decimal digits USD/EUR support. Used for
// rounding and for parsing amount strings into cents. IDR is handled
// separately (0 decimals) — see parseAmountToCents and formatCents.
const decimalPlaces = 2

// smallestUnitFactor = 10^decimalPlaces, used to convert between the
// decimal form (1000.50) and the smallest-unit form (100050).
const smallestUnitFactor = 100

type WalletStatus string

const (
	ACTIVE    WalletStatus = "active"
	SUSPENDED WalletStatus = "suspended"
)

// Wallet represents one wallet, belonging to one owner, in one currency.
// Balance is stored in the smallest unit (cents, for currencies that have
// them) as an int64 — NOT uint64 (so a subtraction bug produces an
// obviously-wrong negative number instead of silently underflowing into
// a huge one) and NOT float64 (binary floating point isn't a fit for
// money).
//
// Balance is really a derived value — the actual source of truth is the
// sum of this wallet's LedgerEntry rows. It's cached here for fast reads,
// but every change to it must go through a ledger entry in the same
// lock/transaction.
type Wallet struct {
	WalletID  string       `json:"wallet_id" example:"a1b2c3d4-e5f6-7890-abcd-1234567890ab"`
	OwnerID   string       `json:"owner_id" example:"a1b2c3d4-e5f6-7890-abcd-1234567890ab"`
	Currency  Currency     `json:"currency" example:"usd"`
	Balance   int64        `json:"-" example:"100.12"` // smallest unit, e.g. cents. Never negative — enforced by Service.
	Status    WalletStatus `json:"status" example:"active"`
	CreatedAt time.Time    `json:"created_at" example:"2026-08-16T09:14:00Z"`
}

func (w Wallet) MarshalJSON() ([]byte, error) {
	type Alias Wallet // hindari infinite recursion
	return json.Marshal(&struct {
		Alias
		Balance string `json:"balance"`
	}{
		Alias:   Alias(w),
		Balance: w.BalanceString(),
	})
}

// BalanceString renders the balance as a decimal string, e.g.
// 100050 -> "1000.50" for USD, or 100050 -> "100050" for IDR (no
// decimals). Only for display — internal math always uses the raw
// int64 smallest-unit value.
func (w *Wallet) BalanceString() string {
	return FormatCents(w.Balance, w.Currency)
}

func FormatCents(cents int64, currency Currency) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}

	sign := ""
	if negative {
		sign = "-"
	}

	if currency != IDR {
		whole := cents / smallestUnitFactor
		frac := cents % smallestUnitFactor
		return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
	}

	return fmt.Sprintf("%s%d", sign, cents)
}
