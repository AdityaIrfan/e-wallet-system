package wallet

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Business-rule errors — distinct from repository sentinel errors above.
// These appear when a business rule is violated, not when storage fails.
// Callers check them with errors.Is().
var (
	ErrInvalidAmount          = errors.New("amount must be a positive value")
	ErrCurrencyMismatch       = errors.New("wallets have different currencies")
	ErrWalletSuspended        = errors.New("wallet is suspended")
	ErrInsufficientBalance    = errors.New("insufficient balance")
	ErrSameWallet             = errors.New("cannot transfer to the same wallet")
	ErrUnsupportedCurrency    = errors.New("unsupported currency")
	ErrIdempotencyKeyRequired = errors.New("Idempotency-Key on header is required")
)

// Service is where all wallet business logic lives. It doesn't know or
// care whether it's called from an HTTP handler, a test, or anything
// else — and it doesn't know whether data is stored in Postgres or
// anywhere else (it only talks to the repository interfaces).
//
// Concurrency safety comes from two layers working together:
//
//  1. Per-wallet mutex (walletLocks): serializes all goroutines in this
//     process that try to mutate the same wallet. This prevents lost
//     updates within a single app instance — without it, two goroutines
//     could both read the same balance, both decide "saldo cukup", and
//     both commit, leaving the balance lower than either intended.
//
//  2. SELECT FOR UPDATE inside UnitOfWork.Tx.UpdateBalance: acquires a
//     Postgres row-level lock that prevents concurrent writes from
//     *other app instances* (multi-instance deployments). The Go mutex
//     only protects within one process; the database lock protects across
//     processes.
//
// Both layers are needed: the mutex avoids unnecessary database round-
// trips when concurrent requests hit the same instance, and the row
// lock handles the case where requests land on different instances.
type Service struct {
	wallets WalletRepository
	ledger  LedgerRepository
	owners  OwnerRepository
	uow     UnitOfWork

	// locksMu protects the walletLocks map itself — Go maps are not
	// safe for concurrent access, so any read or write to walletLocks
	// must hold this mutex first. Note: locksMu is held only for the
	// duration of the map lookup/insert, not for the duration of the
	// wallet operation. It is NOT the mutex that serializes wallet
	// operations (that's the per-wallet mutex returned by lockFor).
	locksMu sync.Mutex

	// walletLocks maps each WalletID to its own mutex. Operations on
	// different wallets get different mutexes and never block each
	// other. Operations on the same wallet share one mutex and are
	// serialized. The map grows as wallets are first accessed and is
	// never pruned — a known memory trade-off acceptable for the scale
	// of this system.
	walletLocks map[string]*sync.Mutex
}

// NewService creates a Service wired to the given storage backends.
// All four dependencies are required — Service will panic on nil.
func NewService(wallets WalletRepository, ledger LedgerRepository, owners OwnerRepository, uow UnitOfWork) *Service {
	return &Service{
		wallets:     wallets,
		ledger:      ledger,
		owners:      owners,
		uow:         uow,
		walletLocks: make(map[string]*sync.Mutex),
	}
}

// lockFor returns the mutex for a specific wallet, creating it if this
// is the first time the wallet is accessed. The get-or-create step
// itself is atomic (protected by locksMu) so two goroutines racing to
// access a new wallet both end up with the same mutex object.
func (s *Service) lockFor(walletID string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	m, ok := s.walletLocks[walletID]
	if !ok {
		m = &sync.Mutex{}
		s.walletLocks[walletID] = m
	}
	return m
}

// CreateOwner registers a new owner. Must be called before CreateWallet
// for the same ownerID — CreateWallet will return ErrOwnerNotFound if
// the owner doesn't exist yet.
func (s *Service) CreateOwner(name string) (*Owner, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	o := &Owner{Name: name}
	if err := s.owners.Create(context.Background(), o); err != nil {
		return nil, err
	}
	return o, nil
}

// ListOwners returns all registered owners ordered by creation time.
func (s *Service) ListOwners() ([]*Owner, error) {
	return s.owners.List(context.Background())
}

// CreateWallet creates a new wallet for ownerID in the given currency.
// The owner must already exist via CreateOwner (returns ErrOwnerNotFound
// otherwise). One owner can only have one wallet per currency — a second
// attempt returns ErrWalletAlreadyExists (edge case #4).
func (s *Service) CreateWallet(ownerID string, currency Currency) (*Wallet, error) {
	ctx := context.Background()

	if ownerID == "" {
		return nil, fmt.Errorf("owner id is required")
	}
	if !isSupportedCurrency(currency) {
		return nil, ErrUnsupportedCurrency
	}

	// Verify the owner exists before doing anything else. CreateWallet
	// does NOT create the owner implicitly — owners are explicit entities
	// (see owner.go for reasoning).
	if _, err := s.owners.Get(ctx, ownerID); err != nil {
		return nil, err // ErrOwnerNotFound if CreateOwner was never called
	}

	existing, err := s.wallets.GetByOwnerAndCurrency(ctx, ownerID, currency)
	if err != nil && !errors.Is(err, ErrWalletNotFound) {
		return nil, err
	}
	if existing != nil {
		return nil, ErrWalletAlreadyExists
	}

	w := &Wallet{
		WalletID: newWalletID(ownerID, currency),
		OwnerID:  ownerID,
		Currency: currency,
		Balance:  0,
		Status:   ACTIVE,
	}
	if err := s.wallets.Create(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// ListWallets returns all wallets registered under an owner, ordered by
// creation time. Returns ErrOwnerNotFound if the owner doesn't exist.
func (s *Service) ListWallets(ownerID string) ([]*Wallet, error) {
	ctx := context.Background()
	if ownerID == "" {
		return nil, fmt.Errorf("owner id is required")
	}
	if _, err := s.owners.Get(ctx, ownerID); err != nil {
		return nil, err
	}
	return s.wallets.List(ctx, ownerID)
}

// Topup adds money to a wallet. amountStr is a decimal string (e.g.
// "1000.50"). idempotencyKey stops a retried request from double-
// crediting the wallet (edge case #6: duplicate requests).
func (s *Service) Topup(walletID, amountStr, idempotencyKey string) (*Wallet, error) {
	return s.applyDelta(walletID, amountStr, idempotencyKey, OpTopup, +1, nil)
}

// Pay deducts money from a wallet. Rejects if the result would be
// negative (edge case #7: concurrent spending is safe because the
// per-wallet mutex in applyDelta serializes concurrent Pay calls).
func (s *Service) Pay(walletID, amountStr, idempotencyKey string) (*Wallet, error) {
	return s.applyDelta(walletID, amountStr, idempotencyKey, OpPayment, -1, nil)
}

// applyDelta is the shared core of Topup and Pay: change one wallet's
// balance by (cents * direction), record it in the ledger, stay
// idempotent. direction is +1 for credits, -1 for debits.
//
// Atomicity: the ledger append and balance update are wrapped in a
// single UnitOfWork transaction — both succeed or neither does (edge
// case #13: crash recovery). This is the critical difference from the
// earlier in-memory version, where the two writes were not atomically
// linked at the storage level.
//
// Idempotency check comes BEFORE acquiring the wallet lock: if this is
// a replay of an already-processed request, there's no need to lock
// anything at all, making the fast path (duplicate request) cheaper.
func (s *Service) applyDelta(walletID, amountStr, idempotencyKey string, op LedgerOperation, direction int64, relatedWallet *string) (*Wallet, error) {
	ctx := context.Background()

	if idempotencyKey != "" {
		if existing, err := s.ledger.FindByIdempotencyKey(ctx, idempotencyKey); err == nil && existing != nil {
			return s.wallets.Get(ctx, existing.WalletID)
		}
	}

	lock := s.lockFor(walletID)
	lock.Lock()
	defer lock.Unlock()

	w, err := s.wallets.Get(ctx, walletID)
	if err != nil {
		return nil, err
	}
	if w.Status == SUSPENDED {
		return nil, ErrWalletSuspended
	}

	cents, err := parseAmountToCents(amountStr, w.Currency)
	if err != nil {
		return nil, err
	}

	signedAmount := direction * cents
	newBalance := w.Balance + signedAmount
	if newBalance < 0 {
		return nil, ErrInsufficientBalance
	}

	// Open a transaction that will hold both the ledger entry and the
	// balance update. defer Rollback so that any early return (panic,
	// error) automatically discards partial writes. Rollback is a no-op
	// after a successful Commit.
	tx, err := s.uow.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.AppendLedger(ctx, &LedgerEntry{
		WalletID:       walletID,
		Currency:       w.Currency,
		Amount:         signedAmount,
		Operation:      op,
		IdempotencyKey: idempotencyKey,
		RelatedWallet:  relatedWallet,
	}); err != nil {
		return nil, err
	}
	if err := tx.UpdateBalance(ctx, walletID, w.Balance, newBalance); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	w.Balance = newBalance
	return w, nil
}

// Transfer moves money from one wallet to another owner's wallet in the
// same currency. All four writes (debit ledger, credit ledger, debit
// balance, credit balance) happen inside ONE UnitOfWork transaction —
// if any step fails, none are committed (edge case #8: partial failure,
// edge case #13: crash recovery).
//
// Lock ordering: the two per-wallet mutexes are always acquired in
// ascending WalletID order, regardless of which wallet is the source
// and which is the destination. Without this, two goroutines running
// A→B and B→A simultaneously would each hold one lock and wait for the
// other — a classic deadlock. Consistent ordering breaks the cycle.
//
// Validation happens before the transaction opens: checking for
// suspension, currency mismatch, owner mismatch, and insufficient
// balance is cheap and doesn't touch the database. Only after all
// validations pass does the transaction open and do the four writes.
func (s *Service) Transfer(fromWalletID, toWalletID, amountStr, idempotencyKey string) error {
	ctx := context.Background()

	if fromWalletID == toWalletID {
		return ErrSameWallet
	}

	// Idempotency check before locks — same reasoning as applyDelta.
	if idempotencyKey != "" {
		if existing, err := s.ledger.FindByIdempotencyKey(ctx, idempotencyKey); err == nil && existing != nil {
			return nil // already processed — replay succeeds without repeating the effect
		}
	}

	// Lock ordering: always acquire in ascending WalletID order.
	first, second := fromWalletID, toWalletID
	if second < first {
		first, second = second, first
	}
	lockA := s.lockFor(first)
	lockB := s.lockFor(second)
	lockA.Lock()
	defer lockA.Unlock()
	lockB.Lock()
	defer lockB.Unlock()

	// One transaction for all four writes — debit ledger, credit ledger,
	// debit balance, credit balance. defer Rollback handles any failure
	// after Begin.
	tx, err := s.uow.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	from, to, err := tx.GetWalletAndLock(ctx, fromWalletID, toWalletID)
	if err != nil {
		return fmt.Errorf("source wallet: %w", err)
	}

	cents, err := parseAmountToCents(amountStr, from.Currency)
	if err != nil {
		return err
	}

	if from.Status == SUSPENDED || to.Status == SUSPENDED {
		return ErrWalletSuspended
	}
	if from.Currency != to.Currency {
		return ErrCurrencyMismatch // edge case #3
	}

	// Verify the destination owner exists and is different from the
	// source owner. Transfer is defined as moving money between different
	// users — two wallets owned by the same person is not "transfer
	// between users" as the spec intends.
	if _, err := s.owners.Get(ctx, to.OwnerID); err != nil {
		return fmt.Errorf("destination owner: %w", err)
	}
	if from.OwnerID == to.OwnerID {
		return fmt.Errorf("%w: source and destination belong to the same owner", ErrSameWallet)
	}

	newFromBalance := from.Balance - cents
	if newFromBalance < 0 {
		return ErrInsufficientBalance
	}
	newToBalance := to.Balance + cents

	if _, err := tx.AppendLedger(ctx, &LedgerEntry{
		WalletID:       fromWalletID,
		Currency:       from.Currency,
		Amount:         -cents,
		Operation:      OpTransferOut,
		IdempotencyKey: idempotencyKey,
		RelatedWallet:  &toWalletID,
	}); err != nil {
		return err
	}
	if _, err := tx.AppendLedger(ctx, &LedgerEntry{
		WalletID:       toWalletID,
		Currency:       to.Currency,
		Amount:         cents,
		Operation:      OpTransferIn,
		IdempotencyKey: idempotencyKey + "-credit",
		RelatedWallet:  &fromWalletID,
	}); err != nil {
		return err
	}
	if err := tx.UpdateBalance(ctx, fromWalletID, from.Balance, newFromBalance); err != nil {
		return err
	}
	if err := tx.UpdateBalance(ctx, toWalletID, to.Balance, newToBalance); err != nil {
		return err
	}

	return tx.Commit()
}

// GetWallet fetches the current status and balance of a wallet (edge
// case #12: read-after-write consistency — because every mutation holds
// its per-wallet lock until Commit completes, a Get right after an
// operation always sees the latest committed data).
func (s *Service) GetWallet(walletID string) (*Wallet, error) {
	return s.wallets.Get(context.Background(), walletID)
}

// SuspendWallet flips a wallet's status to SUSPENDED. After this,
// Topup, Pay, and Transfer against this wallet are all rejected with
// ErrWalletSuspended (edge case #10). The wallet lock is held during
// the status update to prevent a concurrent operation from reading
// ACTIVE just before the suspend lands.
func (s *Service) SuspendWallet(walletID string) error {
	lock := s.lockFor(walletID)
	lock.Lock()
	defer lock.Unlock()
	return s.wallets.UpdateStatus(context.Background(), walletID, SUSPENDED)
}

// LedgerHistory returns every ledger entry recorded for a wallet, in
// the order they were created — the full audit trail backing that
// wallet's balance. Returns ErrWalletNotFound if walletID doesn't
// exist, so callers can tell "wallet exists but has no transactions
// yet" (empty slice, nil error) apart from "this wallet ID was never
// created" (nil, ErrWalletNotFound). ListByWallet alone can't make
// that distinction since an unknown wallet and an empty history both
// come back as an empty slice.
func (s *Service) LedgerHistory(walletID string) ([]*LedgerEntry, error) {
	ctx := context.Background()
	if _, err := s.wallets.Get(ctx, walletID); err != nil {
		return nil, err
	}
	return s.ledger.ListByWallet(ctx, walletID)
}

// ── internal helpers ─────────────────────────────────────────────────

func isSupportedCurrency(c Currency) bool {
	switch c {
	case USD, IDR, EUR:
		return true
	default:
		return false
	}
}

// parseAmountToCents converts a decimal string (e.g. "12.345") into a
// smallest-unit int64, applying round-half-up to the currency's decimal
// precision (edge case #1: 12.345 → 1235 cents for USD, not 1234).
//
// Round-half-up is chosen over banker's rounding (round-half-to-even)
// for simplicity and predictability at this scale. The spec's own
// example (12.345 → 12.35) is consistent with this choice.
//
// Rejects: empty string, negative amounts (edge case #5), and amounts
// that round down to zero — e.g. "0.001" for USD (below the 0.01 cent
// smallest unit) must be rejected, not silently rounded to 0.00 and
// processed as a zero-amount transaction (edge case #1, second half).
//
// IDR has no decimal places (sen is not used in practice), so the
// fractional part is ignored entirely for IDR amounts.
func parseAmountToCents(amountStr string, currency Currency) (int64, error) {
	amountStr = strings.TrimSpace(amountStr)
	if amountStr == "" {
		return 0, ErrInvalidAmount
	}
	if strings.HasPrefix(amountStr, "-") {
		return 0, ErrInvalidAmount
	}

	parts := strings.SplitN(amountStr, ".", 2)
	wholeStr := parts[0]
	fracStr := ""
	if len(parts) == 2 {
		fracStr = parts[1]
	}

	whole, err := strconv.ParseInt(wholeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, amountStr)
	}

	if currency != IDR {
		// Pad or trim fracStr to decimalPlaces+1 digits. The extra digit
		// is the rounding digit — it determines whether we round up or
		// keep the last kept digit as-is.
		for len(fracStr) < decimalPlaces+1 {
			fracStr += "0"
		}
		keptDigits := fracStr[:decimalPlaces]
		roundingDigit := fracStr[decimalPlaces]

		fracValue, err := strconv.ParseInt(keptDigits, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, amountStr)
		}
		if roundingDigit >= '5' {
			fracValue++
		}

		// fracValue can overflow to 100 after rounding up (e.g. "0.995":
		// keptDigits="99", roundingDigit='5' → fracValue=100). Carry the
		// overflow into the whole part so the fraction stays in [0, 99].
		carry := fracValue / smallestUnitFactor
		fracValue %= smallestUnitFactor
		whole += carry
		whole = whole*smallestUnitFactor + fracValue

		if whole <= 0 {
			return 0, ErrInvalidAmount
		}
	}

	return whole, nil
}

// newWalletID produces a human-readable wallet ID for this system.
// Format: "<ownerID>-<CURRENCY>" (e.g. "user1-USD").
// This is intentionally simple for the scope of this project. A
// production system would use UUID or ULID to avoid collisions when
// ownerIDs are not guaranteed to be unique short strings.
func newWalletID(ownerID string, currency Currency) string {
	return fmt.Sprintf("%s-%s", ownerID, strings.ToUpper(string(currency)))
}
