package wallet

import (
	"context"
	"errors"
)

// Sentinel errors — callers (Service and tests) check these with
// errors.Is(). Implementations MUST return one of these for the
// matching condition instead of inventing their own error strings, so
// Service can tell "wallet doesn't exist" apart from "storage is down"
// without parsing error messages.
var (
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrWalletAlreadyExists = errors.New("wallet already exists for this user and currency")
	ErrOptimisticLock      = errors.New("wallet was modified concurrently, retry")
	ErrIdempotencyConflict = errors.New("idempotency key already used with different parameters")
	ErrOwnerNotFound       = errors.New("owner not found")
	ErrOwnerAlreadyExists  = errors.New("owner already exists")
)

// Tx is an atomic unit of work that groups a ledger append and a
// balance update into one all-or-nothing operation. Callers open a Tx
// via UnitOfWork.Begin, perform writes, then Commit or Rollback.
//
// Why this exists: every balance mutation (topup, pay, transfer) must
// write to two places at once — the ledger (source of truth) and the
// wallet's cached balance. If either write fails, the other must be
// undone. Without Tx, a crash between the two writes would leave the
// system in an inconsistent state (ledger says X, balance says Y).
//
// For Postgres: backed by a real *sql.Tx (BEGIN/COMMIT/ROLLBACK), so
// the database guarantees atomicity even across process crashes.
// For in-memory (used in tests): the same interface is satisfied by a
// simple in-process implementation that applies both writes or neither.
type Tx interface {
	GetWalletAndLock(ctx context.Context, walletID, relatedWalletID string) (*Wallet, *Wallet, error)

	// AppendLedger writes one ledger entry inside this transaction.
	// The entry is not visible to other transactions until Commit.
	AppendLedger(ctx context.Context, entry *LedgerEntry) (*LedgerEntry, error)

	// UpdateBalance updates a wallet's balance inside this transaction.
	// Uses SELECT FOR UPDATE in the Postgres implementation to hold a
	// row-level lock until Commit, preventing concurrent transactions
	// from reading a stale balance and both thinking they can proceed.
	// expectedBalance is verified after the lock is acquired — if the
	// stored balance doesn't match, ErrOptimisticLock is returned.
	UpdateBalance(ctx context.Context, walletID string, expectedBalance, newBalance int64) error

	// Commit makes all writes in this transaction permanent and releases
	// any locks held (e.g. the row lock from SELECT FOR UPDATE).
	Commit() error

	// Rollback discards all writes and releases locks. Safe to call
	// even after Commit — it becomes a no-op once the transaction is
	// already done. Always defer Rollback right after Begin so that a
	// panicking or early-returning caller never leaves a dangling
	// transaction open.
	Rollback() error
}

// UnitOfWork is a factory for atomic write units. Service calls Begin
// to open a new Tx, performs writes through it, then Commit or Rollback.
// The interface keeps Service storage-agnostic: it never sees *sql.Tx
// or any other storage-specific type.
type UnitOfWork interface {
	// Begin opens a new atomic transaction. The caller is responsible
	// for calling either Commit or Rollback on the returned Tx.
	Begin(ctx context.Context) (Tx, error)
}

// OwnerRepository is the storage contract for Owner. Kept minimal —
// this is not a user account or auth system, just an identity for
// wallets to attach to. Owners are created EXPLICITLY via Create —
// CreateWallet does not create one on the fly; the owner must already
// exist (returns ErrOwnerNotFound otherwise).
//
// All methods accept context.Context for cancellation, timeout, and
// tracing — even though the in-memory implementation doesn't use it
// actively, keeping the contract consistent avoids interface changes
// when switching to a database-backed implementation.
type OwnerRepository interface {
	// Create stores a new owner. Returns ErrOwnerAlreadyExists if an
	// owner with this ownerID has already been registered.
	Create(ctx context.Context, o *Owner) error

	// Get fetches an existing owner by ID. Returns ErrOwnerNotFound if
	// it was never registered — used by CreateWallet (owner must exist
	// first) and Transfer (verifying the destination wallet really
	// belongs to a different, registered owner).
	Get(ctx context.Context, ownerID string) (*Owner, error)

	// List returns all registered owners ordered by creation time.
	// Backs the GET /owners endpoint.
	List(ctx context.Context) ([]*Owner, error)
}

// WalletRepository is the storage contract for Wallet. It covers reads,
// wallet creation, and status changes. Balance mutations are intentionally
// excluded — those go through UnitOfWork.Tx.UpdateBalance instead, so
// they're always atomic with the corresponding ledger entry.
//
// Service only ever talks to this interface and never knows whether the
// data lives in a Go map or a Postgres table.
type WalletRepository interface {
	// Create stores a new wallet. Returns ErrWalletAlreadyExists if a
	// wallet with the same OwnerID+Currency pair already exists —
	// enforcing the "one wallet per currency per user" rule (edge case #4).
	Create(ctx context.Context, w *Wallet) error

	// List returns all wallets belonging to an owner, ordered by
	// creation time. Backs the GET /wallets?owner_id= endpoint.
	List(ctx context.Context, ownerID string) ([]*Wallet, error)

	// Get fetches a wallet by its ID. Returns ErrWalletNotFound if it
	// doesn't exist.
	Get(ctx context.Context, walletID string) (*Wallet, error)

	// GetByOwnerAndCurrency looks up a wallet by owner + currency pair.
	// Used by CreateWallet to enforce the one-wallet-per-currency
	// constraint, and for lookups where only the owner and currency are
	// known. Returns ErrWalletNotFound if no such wallet exists.
	GetByOwnerAndCurrency(ctx context.Context, ownerID string, currency Currency) (*Wallet, error)

	// UpdateStatus changes a wallet's status (e.g. ACTIVE → SUSPENDED).
	// Does not touch balance or ledger — status changes are not financial
	// events, so they don't need to be wrapped in a UnitOfWork.
	// Returns ErrWalletNotFound if the wallet doesn't exist.
	UpdateStatus(ctx context.Context, walletID string, status WalletStatus) error
}

// LedgerRepository is the read side of the ledger. Writes go through
// UnitOfWork.Tx.AppendLedger instead, ensuring they are always paired
// atomically with a balance update.
//
// The ledger is append-only by design — there is deliberately no Update
// or Delete method here. An audit trail that can be modified is not an
// audit trail that can be trusted.
type LedgerRepository interface {
	// SumByWallet returns SUM(amount) across all entries for a wallet.
	// Used for reconciliation: verifying that Wallet.Balance always
	// equals SumByWallet(walletID) (edge case #9).
	SumByWallet(ctx context.Context, walletID string) (int64, error)

	// ListByWallet returns all entries for a wallet ordered by creation
	// time — the full audit trail. Backs the GET /wallets/:id/ledger
	// endpoint and LedgerHistory in Service.
	ListByWallet(ctx context.Context, walletID string) ([]*LedgerEntry, error)

	// FindByIdempotencyKey looks up an entry by its idempotency key
	// without needing to know the WalletID up front. Service calls this
	// before acquiring any wallet lock — if a matching entry is found,
	// the operation is a replay and can return early without touching
	// any state (edge case #6: duplicate requests).
	// Returns (nil, nil) if no matching entry exists — not found is not
	// an error here, it just means this is a new request.
	FindByIdempotencyKey(ctx context.Context, key string) (*LedgerEntry, error)
}
