package wallet

import (
	"encoding/json"
	"time"
)

// LedgerOperation tags what kind of entry this is, for the audit trail.
type LedgerOperation string

const (
	OpTopup       LedgerOperation = "topup"
	OpPayment     LedgerOperation = "payment"
	OpTransferOut LedgerOperation = "transfer_out"
	OpTransferIn  LedgerOperation = "transfer_in"
)

// LedgerEntry is one append-only line in the audit trail. A wallet's
// balance should always be reconstructable from SUM(Amount) across all
// LedgerEntry rows for that WalletID.
//
// Amount is signed: positive for credits (topup, transfer_in), negative
// for debits (payment, transfer_out). That's what lets a plain SUM(Amount)
// give you the balance without needing to know what kind of operation
// each entry was.
type LedgerEntry struct {
	EntryID        string          `json:"-"`
	WalletID       string          `json:"wallet_id"`
	Currency       Currency        `json:"currency"`
	Amount         int64           `json:"-"` // signed, smallest unit. Positive = credit, negative = debit.
	Operation      LedgerOperation `json:"operation"`
	IdempotencyKey string          `json:"-"`
	RelatedWallet  *string         `json:"related_wallet"` // for transfers: the other wallet involved. Empty for topup/payment.
	CreatedAt      time.Time       `json:"created_time"`
}

func (l LedgerEntry) MarshalJSON() ([]byte, error) {
	type Alias LedgerEntry // hindari infinite recursion
	return json.Marshal(&struct {
		Alias
		Amount string `json:"amount"`
	}{
		Alias:  Alias(l),
		Amount: FormatCents(l.Amount, l.Currency),
	})
}
