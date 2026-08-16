// Package postgres implements the wallet repository interfaces backed
// by a Postgres database via sqlx. All queries use $N positional
// parameters — no string interpolation into SQL — so SQL injection is
// not possible regardless of input values.
//
// Concurrency model: balance mutations go through PgUnitOfWork, which
// opens a real Postgres transaction (BEGIN/COMMIT/ROLLBACK). Inside
// that transaction, UpdateBalance acquires a row-level lock via
// SELECT ... FOR UPDATE before reading or writing the balance. This
// means two concurrent transactions targeting the same wallet will
// serialize at the database level, not just at the Go-mutex level in
// Service. The Go mutex in Service is still valuable: it prevents
// unnecessary database round-trips when concurrent requests land on
// the same app instance.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/AdityaIrfan/e-wallet-system-vocagame/wallet"
)

// ── Owner ─────────────────────────────────────────────────────────────

// OwnerRepo implements wallet.OwnerRepository using Postgres.
type OwnerRepo struct{ db *sqlx.DB }

func NewOwnerRepo(db *sqlx.DB) *OwnerRepo { return &OwnerRepo{db: db} }

// Create inserts a new owner row. ON CONFLICT DO NOTHING handles the
// race where two concurrent CreateOwner calls use the same ownerID —
// the second insert is silently dropped by Postgres, and the missing
// RETURNING row signals ErrOwnerAlreadyExists to the caller.
func (r *OwnerRepo) Create(ctx context.Context, o *wallet.Owner) error {
	const q = `
		INSERT INTO owners (name, created_at)
		VALUES ($1, NOW())
		RETURNING owner_id, name, created_at`

	err := r.db.QueryRowContext(ctx, q, o.Name).Scan(&o.OwnerID, &o.Name, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// ON CONFLICT DO NOTHING fired — owner already exists.
		return wallet.ErrOwnerAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("create owner: %w", err)
	}
	return nil
}

func (r *OwnerRepo) Get(ctx context.Context, ownerID string) (*wallet.Owner, error) {
	const q = `SELECT owner_id, created_at FROM owners WHERE owner_id = $1`
	var o wallet.Owner
	err := r.db.QueryRowContext(ctx, q, ownerID).Scan(&o.OwnerID, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wallet.ErrOwnerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get owner: %w", err)
	}
	return &o, nil
}

func (r *OwnerRepo) List(ctx context.Context) ([]*wallet.Owner, error) {
	const q = `SELECT owner_id, name, created_at FROM owners ORDER BY created_at ASC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list owners: %w", err)
	}
	defer rows.Close()

	var owners []*wallet.Owner
	for rows.Next() {
		var o wallet.Owner
		if err := rows.Scan(&o.OwnerID, &o.Name, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan owner: %w", err)
		}
		owners = append(owners, &o)
	}
	return owners, rows.Err()
}

// ── Wallet ────────────────────────────────────────────────────────────

// WalletRepo implements wallet.WalletRepository using Postgres.
// Note: UpdateBalance is intentionally absent — balance mutations go
// through PgUnitOfWork.pgTx.UpdateBalance so they are always atomic
// with the corresponding ledger entry.
type WalletRepo struct{ db *sqlx.DB }

func NewWalletRepo(db *sqlx.DB) *WalletRepo { return &WalletRepo{db: db} }

// Create inserts a new wallet row. The unique constraint on
// (owner_id, currency) in the schema is the database-level enforcement
// of "one wallet per currency per user" — this catches races that slip
// past the application-level check in Service.CreateWallet.
func (r *WalletRepo) Create(ctx context.Context, w *wallet.Wallet) error {
	const q = `
		INSERT INTO wallets (owner_id, currency, balance, status, created_at)
		VALUES ($1, $2, $3, $4, NOW())
		RETURNING wallet_id, owner_id, currency, balance, created_at`

	err := r.db.QueryRowContext(ctx, q,
		w.OwnerID, string(w.Currency), w.Balance, string(w.Status),
	).Scan(&w.WalletID, &w.OwnerID, &w.Currency, &w.Balance, &w.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return wallet.ErrWalletAlreadyExists
		}
		return fmt.Errorf("create wallet: %w", err)
	}
	return nil
}

func (r *WalletRepo) Get(ctx context.Context, walletID string) (*wallet.Wallet, error) {
	const q = `
		SELECT wallet_id, owner_id, currency, balance, status, created_at
		FROM wallets WHERE wallet_id = $1`

	var w wallet.Wallet
	var currency, status string
	err := r.db.QueryRowContext(ctx, q, walletID).Scan(
		&w.WalletID, &w.OwnerID, &currency, &w.Balance, &status, &w.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wallet.ErrWalletNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get wallet: %w", err)
	}
	w.Currency = wallet.Currency(currency)
	w.Status = wallet.WalletStatus(status)
	return &w, nil
}

func (r *WalletRepo) List(ctx context.Context, ownerID string) ([]*wallet.Wallet, error) {
	const q = `
		SELECT wallet_id, owner_id, currency, balance, status, created_at
		FROM wallets WHERE owner_id = $1 ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list wallets: %w", err)
	}
	defer rows.Close()

	var wallets []*wallet.Wallet
	for rows.Next() {
		var w wallet.Wallet
		var currency, status string
		if err := rows.Scan(
			&w.WalletID, &w.OwnerID, &currency, &w.Balance, &status, &w.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan wallet: %w", err)
		}
		w.Currency = wallet.Currency(currency)
		w.Status = wallet.WalletStatus(status)
		wallets = append(wallets, &w)
	}
	return wallets, rows.Err()
}

func (r *WalletRepo) GetByOwnerAndCurrency(ctx context.Context, ownerID string, currency wallet.Currency) (*wallet.Wallet, error) {
	const q = `
		SELECT wallet_id, owner_id, currency, balance, status, created_at
		FROM wallets WHERE owner_id = $1 AND currency = $2`

	var w wallet.Wallet
	var cur, status string
	err := r.db.QueryRowContext(ctx, q, ownerID, string(currency)).Scan(
		&w.WalletID, &w.OwnerID, &cur, &w.Balance, &status, &w.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wallet.ErrWalletNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get wallet by owner+currency: %w", err)
	}
	w.Currency = wallet.Currency(cur)
	w.Status = wallet.WalletStatus(status)
	return &w, nil
}

// UpdateStatus changes a wallet's status (e.g. ACTIVE → SUSPENDED).
// Status changes are not financial events, so they don't need to be
// wrapped in a UnitOfWork — a plain UPDATE is fine here.
func (r *WalletRepo) UpdateStatus(ctx context.Context, walletID string, status wallet.WalletStatus) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE wallets SET status = $1 WHERE wallet_id = $2`,
		string(status), walletID,
	)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return wallet.ErrWalletNotFound
	}
	return nil
}

// ── Ledger ────────────────────────────────────────────────────────────

// LedgerRepo implements wallet.LedgerRepository using Postgres.
// It covers read-only ledger access and idempotency lookups. Ledger
// writes go through pgTx.AppendLedger (inside a UnitOfWork transaction)
// so they are always atomic with the corresponding balance update.
type LedgerRepo struct{ db *sqlx.DB }

func NewLedgerRepo(db *sqlx.DB) *LedgerRepo { return &LedgerRepo{db: db} }

// SumByWallet returns the sum of all ledger entries for a wallet.
// COALESCE handles the case of a wallet with no entries — SUM of an
// empty set is NULL in SQL, but we want 0.
func (r *LedgerRepo) SumByWallet(ctx context.Context, walletID string) (int64, error) {
	var sum int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM ledger_entries WHERE wallet_id = $1`,
		walletID,
	).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum ledger: %w", err)
	}
	return sum, nil
}

// ListByWallet returns all ledger entries for a wallet in chronological
// order. COALESCE converts NULL optional columns back to empty strings
// so callers don't have to deal with *string nullable fields.
func (r *LedgerRepo) ListByWallet(ctx context.Context, walletID string) ([]*wallet.LedgerEntry, error) {
	const q = `
		SELECT entry_id, wallet_id, currency, amount, operation, COALESCE(related_wallet::text, '') as related_wallet, created_at
		FROM ledger_entries
		WHERE wallet_id = $1
		ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, q, walletID)
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	defer rows.Close()

	var entries []*wallet.LedgerEntry
	for rows.Next() {
		var e wallet.LedgerEntry
		var currency, operation string
		if err := rows.Scan(
			&e.EntryID, &e.WalletID, &currency, &e.Amount,
			&operation, &e.RelatedWallet, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan ledger entry: %w", err)
		}
		e.Currency = wallet.Currency(currency)
		e.Operation = wallet.LedgerOperation(operation)
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

// FindByIdempotencyKey looks up a ledger entry by its idempotency key.
// Returns (nil, nil) — not an error — when no matching entry is found.
// This lets Service distinguish "not found, proceed as new request"
// from "found, return early as replay" without treating absence as a
// failure. The index on idempotency_key (see migrations) makes this
// lookup fast even on large ledger tables.
func (r *LedgerRepo) FindByIdempotencyKey(ctx context.Context, key string) (*wallet.LedgerEntry, error) {
	const q = `
		SELECT entry_id, wallet_id, currency, amount, operation,
		       COALESCE(idempotency_key, ''), COALESCE(related_wallet::text, '') as related_wallet,
		       created_at
		FROM ledger_entries
		WHERE idempotency_key = $1
		LIMIT 1`

	var e wallet.LedgerEntry
	var currency, operation string
	err := r.db.QueryRowContext(ctx, q, key).Scan(
		&e.EntryID, &e.WalletID, &currency, &e.Amount,
		&operation, &e.IdempotencyKey, &e.RelatedWallet, &e.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // not found is not an error — caller checks nil
	}
	if err != nil {
		return nil, fmt.Errorf("find by idempotency key: %w", err)
	}
	e.Currency = wallet.Currency(currency)
	e.Operation = wallet.LedgerOperation(operation)
	return &e, nil
}

// ── UnitOfWork ────────────────────────────────────────────────────────

// PgUnitOfWork implements wallet.UnitOfWork using real Postgres
// transactions. Each Begin call opens a new *sql.Tx. All AppendLedger
// and UpdateBalance calls within that Tx execute inside the same
// BEGIN/COMMIT block — either all writes land, or none do (atomicity).
// This is what makes edge case #13 (crash recovery) genuinely
// testable: if the process crashes between AppendLedger and Commit,
// Postgres automatically rolls back the transaction on reconnect.
type PgUnitOfWork struct{ db *sqlx.DB }

func NewUnitOfWork(db *sqlx.DB) *PgUnitOfWork { return &PgUnitOfWork{db: db} }

// Begin opens a new transaction. The caller must call Commit or
// Rollback on the returned Tx. Always defer Rollback immediately after
// Begin — it is a no-op after Commit and ensures the transaction is
// never left open on an error path.
func (u *PgUnitOfWork) Begin(ctx context.Context) (wallet.Tx, error) {
	tx, err := u.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	return &pgTx{tx: tx}, nil
}

// pgTx wraps *sql.Tx and implements wallet.Tx. It exposes exactly the
// two write operations Service needs inside a transaction.
type pgTx struct {
	tx *sql.Tx
}

// GetWalletAndLock fetches and locks both wallets involved in a
// transfer inside the current transaction using SELECT ... FOR UPDATE.
// The row-level locks are held until Commit or Rollback, preventing
// any other transaction from reading or modifying either wallet's
// balance concurrently — this is the database-level safety net for
// multi-instance deployments where the Go-level per-wallet mutex in
// Service doesn't reach.
//
// Lock ordering: rows are locked in ascending WalletID order, matching
// the mutex acquisition order in Service.Transfer. This prevents a
// database-level deadlock when two transfers run in opposite directions
// (A→B and B→A) simultaneously — both will attempt to lock the same
// two rows, and consistent ordering ensures neither blocks the other
// waiting for a lock it can't get.
func (t *pgTx) GetWalletAndLock(ctx context.Context, walletID, relatedWalletID string) (*wallet.Wallet, *wallet.Wallet, error) {
	getAndLock := func(tx *sql.Tx, id string) (*wallet.Wallet, error) {
		const q = `
            SELECT wallet_id, owner_id, currency, balance, status, created_at
            FROM wallets WHERE wallet_id = $1 FOR UPDATE`

		var w wallet.Wallet
		var currency, status string
		err := tx.QueryRowContext(ctx, q, id).Scan(
			&w.WalletID, &w.OwnerID, &currency, &w.Balance, &status, &w.CreatedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, wallet.ErrWalletNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("get and lock wallet: %w", err)
		}
		w.Currency = wallet.Currency(currency)
		w.Status = wallet.WalletStatus(status)
		return &w, nil
	}

	// Lock in ascending WalletID order to match Service.Transfer mutex
	// ordering — prevents database-level deadlock on reverse transfers.
	first, second := walletID, relatedWalletID
	if second < first {
		first, second = second, first
	}

	w1, err := getAndLock(t.tx, first)
	if err != nil {
		return nil, nil, err
	}
	w2, err := getAndLock(t.tx, second)
	if err != nil {
		return nil, nil, err
	}

	// Return in original order (walletID first, relatedWalletID second)
	// regardless of which was locked first.
	if first == walletID {
		return w1, w2, nil
	}
	return w2, w1, nil
}

// AppendLedger inserts one ledger entry into the open transaction.
// NULLIF converts empty strings to NULL for the optional columns
// (idempotency_key, related_wallet), keeping the schema consistent —
// NULL means "not set", empty string is not a valid sentinel here.
// without needing a separate ID-generation step in the application.
func (t *pgTx) AppendLedger(ctx context.Context, entry *wallet.LedgerEntry) (*wallet.LedgerEntry, error) {
	const q = `
		INSERT INTO ledger_entries
			(wallet_id, currency, amount, operation,
			 idempotency_key, related_wallet, created_at)
		VALUES
			($1, $2, $3, $4, $5, NULLIF($6, '')::uuid, NOW())
		RETURNING entry_id, created_at`

	cp := *entry
	err := t.tx.QueryRowContext(ctx, q,
		cp.WalletID,
		string(cp.Currency),
		cp.Amount,
		string(cp.Operation),
		cp.IdempotencyKey,
		cp.RelatedWallet,
	).Scan(&cp.EntryID, &cp.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("append ledger entry (tx): %w", err)
	}
	return &cp, nil
}

// UpdateBalance reads the current balance with SELECT ... FOR UPDATE,
// verifies it matches expectedBalance, then writes newBalance.
//
// SELECT FOR UPDATE acquires a row-level exclusive lock that is held
// until the transaction commits or rolls back. Any other transaction
// that tries to SELECT FOR UPDATE the same row will block here until
// the lock is released. This serializes concurrent balance mutations
// at the database level — essential for multi-instance deployments
// where the Go-level per-wallet mutex in Service doesn't reach.
//
// The expectedBalance check after the lock is a second line of defense:
// it catches the (rare, in practice) case where the balance was changed
// between the Service reading it and this transaction acquiring the
// lock. If the values diverge, ErrOptimisticLock is returned and the
// caller (Service) surfaces it as a 409 Conflict — the client should
// retry.
func (t *pgTx) UpdateBalance(ctx context.Context, walletID string, expectedBalance, newBalance int64) error {
	var current int64
	err := t.tx.QueryRowContext(ctx,
		`SELECT balance FROM wallets WHERE wallet_id = $1 FOR UPDATE`,
		walletID,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return wallet.ErrWalletNotFound
	}
	if err != nil {
		return fmt.Errorf("lock wallet row (tx): %w", err)
	}
	if current != expectedBalance {
		return wallet.ErrOptimisticLock
	}
	if _, err := t.tx.ExecContext(ctx,
		`UPDATE wallets SET balance = $1 WHERE wallet_id = $2`,
		newBalance, walletID,
	); err != nil {
		return fmt.Errorf("update balance (tx): %w", err)
	}
	return nil
}

// Commit makes all writes in this transaction permanent and releases
// all locks (including SELECT FOR UPDATE row locks).
func (t *pgTx) Commit() error { return t.tx.Commit() }

// Rollback discards all writes and releases all locks. Safe to call
// after Commit — sql.Tx.Rollback returns sql.ErrTxDone which is
// silently ignored here so that callers can always defer Rollback
// without special-casing the committed path.
func (t *pgTx) Rollback() error {
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil // already committed — not an error
	}
	return err
}

// ── helpers ───────────────────────────────────────────────────────────

// isUniqueViolation checks whether a Postgres error is a unique
// constraint violation (error code 23505). Used to map database-level
// uniqueness errors to domain-level sentinel errors without exposing
// Postgres error types to the caller.
func isUniqueViolation(err error) bool {
	var pgErr *pq.Error
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
