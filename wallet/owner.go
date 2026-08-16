package wallet

import "time"

// Owner represents whoever owns a wallet. Kept deliberately minimal —
// this isn't a user account/auth system (the assignment doesn't ask for
// one), just an identity for wallets to attach to. Owners are created
// EXPLICITLY via Create — CreateWallet won't create one on the fly, it
// has to already exist (returns ErrOwnerNotFound otherwise).
type Owner struct {
	OwnerID   string    `json:"owner_id" example:"a1b2c3d4-e5f6-7890-abcd-1234567890ab"`
	Name      string    `json:"name" example:"John Doe"`
	CreatedAt time.Time `json:"created_at" example:"2026-08-16T09:14:00Z"`
}
