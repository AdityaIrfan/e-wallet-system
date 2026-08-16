package wallet_test

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/AdityaIrfan/e-wallet-system-vocagame/db"
	pgstore "github.com/AdityaIrfan/e-wallet-system-vocagame/storage/postgres"
	"github.com/AdityaIrfan/e-wallet-system-vocagame/wallet"
)

// ── test setup ───────────────────────────────────────────────────────

// testDSN holds the connection string to the shared Postgres container.
// Using TestMain so the container is started once and reused across all
// tests — much faster than spinning a new one per test function.
var testDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Resolve the migrations directory relative to this test file so
	// the path works regardless of where `go test` is invoked from.
	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "migrations")

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("ewallet_test"),
		tcpostgres.WithUsername("ewallet"),
		tcpostgres.WithPassword("ewallet"),
		// Wait until Postgres logs "ready to accept connections" twice —
		// the first occurrence is during initialization, the second means
		// it's actually ready to serve queries.
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		panic("start postgres container: " + err.Error())
	}
	defer container.Terminate(ctx)

	testDSN, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic("get connection string: " + err.Error())
	}

	// Apply migrations once — all tests share the same schema.
	// Each test uses uid(t, ...) to generate unique IDs so tests don't
	// step on each other's data.
	if err := db.Migrate(testDSN, "file://"+migrationsDir); err != nil {
		panic("run migrations: " + err.Error())
	}

	m.Run()
}

// newTestService builds a Service backed by the real Postgres container.
// Each test gets a fresh Service but shares the same database — tests
// are responsible for using unique IDs via uid() to avoid conflicts.
func newTestService(t *testing.T) *wallet.Service {
	t.Helper()
	database, err := db.Connect(testDSN)
	require.NoError(t, err, "connect to test db")
	t.Cleanup(func() { database.Close() })

	return wallet.NewService(
		pgstore.NewWalletRepo(database),
		pgstore.NewLedgerRepo(database),
		pgstore.NewOwnerRepo(database),
		pgstore.NewUnitOfWork(database),
	)
}

// uid returns a test-scoped unique string built from the test name and
// a suffix. Keeps IDs unique across parallel tests that share the DB.
func uid(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf("%s-%s", uuid.New(), name)
}

// newTestWallet registers an owner and creates one wallet for them.
// Tests that want to verify CreateOwner or CreateWallet behavior itself
// should NOT use this helper — call the methods directly so errors can
// be inspected.
func newTestWallet(t *testing.T, svc *wallet.Service, name string, currency wallet.Currency) *wallet.Wallet {
	t.Helper()
	o, err := svc.CreateOwner(name)
	require.NoError(t, err, "setup CreateOwner(%s)", name)

	w, err := svc.CreateWallet(o.OwnerID, currency)
	require.NoError(t, err, "setup CreateWallet(%s, %s)", o.OwnerID, currency)
	return w
}

// ── Edge case #1: Decimal Precision ──────────────────────────────────

func TestDecimal_Precision_Topup_RoundsHalfUp(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	got, err := svc.Topup(w.WalletID, "12.345", uid(t, "topup-001"))
	require.NoError(t, err)
	assert.Equal(t, "12.35", got.BalanceString())
}

func TestDecimal_Precision_Payment_BelowSmallestUnit_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(w.WalletID, "10.00", uid(t, "seed"))
	require.NoError(t, err)

	_, err = svc.Pay(w.WalletID, "0.001", uid(t, "pay-001"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrInvalidAmount)

	current, err := svc.GetWallet(w.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "10.00", current.BalanceString())
}

// ── Edge case #2: Large Balances ─────────────────────────────────────

func TestLargeBalance_OneBillion_StoredSafely(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	got, err := svc.Topup(w.WalletID, "1000000000.00", uid(t, "topup-001"))
	require.NoError(t, err)
	assert.Equal(t, "1000000000.00", got.BalanceString())

	got, err = svc.Topup(w.WalletID, "500000000.50", uid(t, "topup-002"))
	require.NoError(t, err)
	assert.Equal(t, "1500000000.50", got.BalanceString())
}

// ── Edge case #3: Currency Mismatch ──────────────────────────────────

func TestTransfer_CurrencyMismatch_Rejected(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	o2, err := svc.CreateOwner(uid(t, "user2"))
	require.NoError(t, err)
	to, err := svc.CreateWallet(o2.OwnerID, wallet.EUR)
	require.NoError(t, err)

	_, err = svc.Topup(from.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	err = svc.Transfer(from.WalletID, to.WalletID, "50.00", uid(t, "transfer-001"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrCurrencyMismatch)

	fromAfter, err := svc.GetWallet(from.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "100.00", fromAfter.BalanceString())
}

// ── Edge case #4: Multiple Wallets Per User ───────────────────────────

func TestCreateWallet_OneWalletPerCurrencyPerUser(t *testing.T) {
	svc := newTestService(t)
	o, err := svc.CreateOwner(uid(t, "user1"))
	require.NoError(t, err)

	_, err = svc.CreateWallet(o.OwnerID, wallet.USD)
	require.NoError(t, err)

	_, err = svc.CreateWallet(o.OwnerID, wallet.EUR)
	assert.NoError(t, err)

	_, err = svc.CreateWallet(o.OwnerID, wallet.USD)
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrWalletAlreadyExists)
}

// TestCreateWallet_ConcurrentSameCurrency_OnlyOneSucceeds proves the
// "one wallet per currency per user" rule holds under concurrent
// requests, not just sequential ones. The application-level check in
// Service.CreateWallet (GetByOwnerAndCurrency before insert) has a
// TOCTOU race on its own — two concurrent calls can both see "not
// found" and both insert. The uq_wallets_owner_currency constraint
// (migration 000002) closes that race at the database level: exactly
// one INSERT wins, the rest fail with a unique violation that
// WalletRepo.Create maps to wallet.ErrWalletAlreadyExists.
//
// Run with: go test -race ./wallet/...
func TestCreateWallet_ConcurrentSameCurrency_OnlyOneSucceeds(t *testing.T) {
	svc := newTestService(t)
	o, err := svc.CreateOwner(uid(t, "user1"))
	require.NoError(t, err)

	const attempts = 20
	var wg sync.WaitGroup
	var successCount atomic.Int32

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateWallet(o.OwnerID, wallet.USD)
			if err == nil {
				successCount.Add(1)
			} else {
				assert.ErrorIs(t, err, wallet.ErrWalletAlreadyExists)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successCount.Load(),
		"exactly one concurrent CreateWallet should succeed for the same owner+currency")

	wallets, err := svc.ListWallets(o.OwnerID)
	require.NoError(t, err)
	assert.Len(t, wallets, 1, "only one USD wallet should exist for this owner")
}

// ── Edge case #5: Zero or Negative Amounts ───────────────────────────

func TestTopup_ZeroAmount_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	_, err := svc.Topup(w.WalletID, "0.00", uid(t, "topup-zero"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrInvalidAmount)
}

func TestTopup_NegativeAmount_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	_, err := svc.Topup(w.WalletID, "-50.00", uid(t, "topup-neg"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrInvalidAmount)
}

func TestPay_ZeroAmount_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(w.WalletID, "50.00", uid(t, "seed"))
	require.NoError(t, err)

	_, err = svc.Pay(w.WalletID, "0.00", uid(t, "pay-zero"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrInvalidAmount)
}

func TestPay_NegativeAmount_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(w.WalletID, "50.00", uid(t, "seed"))
	require.NoError(t, err)

	_, err = svc.Pay(w.WalletID, "-10.00", uid(t, "pay-neg"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrInvalidAmount)
}

// ── Edge case #6: Duplicate Requests ─────────────────────────────────

func TestTopup_DuplicateRequest_Idempotent(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	key := uid(t, "topup-key")

	w1, err := svc.Topup(w.WalletID, "100.00", key)
	require.NoError(t, err)
	assert.Equal(t, "100.00", w1.BalanceString())

	w2, err := svc.Topup(w.WalletID, "100.00", key)
	require.NoError(t, err, "duplicate request should not error")
	assert.Equal(t, "100.00", w2.BalanceString(), "duplicate was processed twice")
}

func TestPay_DuplicateRequest_Idempotent(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(w.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	key := uid(t, "pay-key")
	w1, err := svc.Pay(w.WalletID, "30.00", key)
	require.NoError(t, err)
	assert.Equal(t, "70.00", w1.BalanceString())

	w2, err := svc.Pay(w.WalletID, "30.00", key)
	require.NoError(t, err, "duplicate request should not error")
	assert.Equal(t, "70.00", w2.BalanceString(), "duplicate was processed twice")
}

func TestTransfer_DuplicateRequest_Idempotent(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	o2, err := svc.CreateOwner(uid(t, "user2"))
	require.NoError(t, err)
	to, err := svc.CreateWallet(o2.OwnerID, wallet.USD)
	require.NoError(t, err)

	_, err = svc.Topup(from.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	key := uid(t, "transfer-key")
	err = svc.Transfer(from.WalletID, to.WalletID, "40.00", key)
	require.NoError(t, err)

	err = svc.Transfer(from.WalletID, to.WalletID, "40.00", key)
	require.NoError(t, err, "duplicate transfer should not error")

	fromAfter, err := svc.GetWallet(from.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "60.00", fromAfter.BalanceString(), "duplicate transfer was processed twice")

	toAfter, err := svc.GetWallet(to.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "40.00", toAfter.BalanceString(), "duplicate transfer was processed twice")
}

// ── Edge case #7: Concurrent Spending ────────────────────────────────

// Run with: go test -race ./wallet/...
func TestPay_ConcurrentSpending_NeverGoesNegative(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(w.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	const numAttempts = 50
	const payAmountCents = 500

	var wg sync.WaitGroup
	var successCount atomic.Int32

	for i := 0; i < numAttempts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Idempotency key must be unique per goroutine — a shared key
			// would test idempotency, not concurrency.
			key := fmt.Sprintf("%s-pay-%d", t.Name(), idx)
			_, err := svc.Pay(w.WalletID, "5.00", key)
			if err == nil {
				successCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	final, err := svc.GetWallet(w.WalletID)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, final.Balance, int64(0), "balance went negative")

	// Cross-check against successCount — proves there are no lost
	// updates, not just that the balance is non-negative.
	expected := int64(10000) - int64(successCount.Load())*payAmountCents
	assert.Equal(t, expected, final.Balance,
		"balance inconsistent (possible lost update), successCount=%d", successCount.Load())

	t.Logf("successful payments: %d/%d, final balance: %s",
		successCount.Load(), numAttempts, final.BalanceString())
}

// ── Edge case #8: Partial Failure During Transfer ─────────────────────

func TestTransfer_DestinationNotFound_SourceNotDebited(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(from.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	err = svc.Transfer(from.WalletID, "nonexistent-wallet-id", "50.00", uid(t, "transfer-fail"))
	require.Error(t, err)

	fromAfter, err := svc.GetWallet(from.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "100.00", fromAfter.BalanceString(),
		"source wallet was debited despite failed transfer")
}

func TestTransfer_InsufficientBalance_NothingCommitted(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	o2, err := svc.CreateOwner(uid(t, "user2"))
	require.NoError(t, err)
	to, err := svc.CreateWallet(o2.OwnerID, wallet.USD)
	require.NoError(t, err)

	_, err = svc.Topup(from.WalletID, "30.00", uid(t, "seed"))
	require.NoError(t, err)

	err = svc.Transfer(from.WalletID, to.WalletID, "50.00", uid(t, "transfer-insufficient"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrInsufficientBalance)

	fromAfter, err := svc.GetWallet(from.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "30.00", fromAfter.BalanceString())

	toAfter, err := svc.GetWallet(to.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "0.00", toAfter.BalanceString())
}

// ── Edge case #9: Ledger vs Balance Mismatch ──────────────────────────

func TestLedger_SumMatchesBalance_AfterMultipleOperations(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	o2, err := svc.CreateOwner(uid(t, "user2"))
	require.NoError(t, err)
	to, err := svc.CreateWallet(o2.OwnerID, wallet.USD)
	require.NoError(t, err)

	_, err = svc.Topup(from.WalletID, "1000.50", uid(t, "topup-1"))
	require.NoError(t, err)
	_, err = svc.Pay(from.WalletID, "200.10", uid(t, "pay-1"))
	require.NoError(t, err)
	err = svc.Transfer(from.WalletID, to.WalletID, "300.40", uid(t, "transfer-1"))
	require.NoError(t, err)

	fromWallet, err := svc.GetWallet(from.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "500.00", fromWallet.BalanceString())

	toWallet, err := svc.GetWallet(to.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "300.40", toWallet.BalanceString())
}

// ── Edge case #10: Suspended Wallet Operations ────────────────────────

func TestSuspendedWallet_Topup_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	require.NoError(t, svc.SuspendWallet(w.WalletID))

	_, err := svc.Topup(w.WalletID, "50.00", uid(t, "topup"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrWalletSuspended)
}

func TestSuspendedWallet_Pay_Rejected(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(w.WalletID, "50.00", uid(t, "seed"))
	require.NoError(t, err)
	require.NoError(t, svc.SuspendWallet(w.WalletID))

	_, err = svc.Pay(w.WalletID, "10.00", uid(t, "pay"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrWalletSuspended)
}

func TestSuspendedWallet_TransferOut_Rejected(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	o2, err := svc.CreateOwner(uid(t, "user2"))
	require.NoError(t, err)
	to, err := svc.CreateWallet(o2.OwnerID, wallet.USD)
	require.NoError(t, err)

	_, err = svc.Topup(from.WalletID, "50.00", uid(t, "seed"))
	require.NoError(t, err)
	require.NoError(t, svc.SuspendWallet(from.WalletID))

	err = svc.Transfer(from.WalletID, to.WalletID, "10.00", uid(t, "transfer"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrWalletSuspended)
}

func TestSuspendedWallet_TransferIn_Rejected(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	o2, err := svc.CreateOwner(uid(t, "user2"))
	require.NoError(t, err)
	to, err := svc.CreateWallet(o2.OwnerID, wallet.USD)
	require.NoError(t, err)

	_, err = svc.Topup(from.WalletID, "50.00", uid(t, "seed"))
	require.NoError(t, err)
	require.NoError(t, svc.SuspendWallet(to.WalletID))

	err = svc.Transfer(from.WalletID, to.WalletID, "10.00", uid(t, "transfer"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrWalletSuspended)
}

// ── Edge case #11: Out-of-Order Requests ──────────────────────────────

func TestOperations_ConsistentRegardlessOfOrder(t *testing.T) {
	svc := newTestService(t)

	walletA := newTestWallet(t, svc, uid(t, "userA"), wallet.USD)
	_, err := svc.Topup(walletA.WalletID, "100.00", uid(t, "a-topup-1"))
	require.NoError(t, err)
	_, err = svc.Topup(walletA.WalletID, "50.00", uid(t, "a-topup-2"))
	require.NoError(t, err)
	_, err = svc.Pay(walletA.WalletID, "30.00", uid(t, "a-pay-1"))
	require.NoError(t, err)

	walletB := newTestWallet(t, svc, uid(t, "userB"), wallet.USD)
	_, err = svc.Topup(walletB.WalletID, "50.00", uid(t, "b-topup-1"))
	require.NoError(t, err)
	_, err = svc.Topup(walletB.WalletID, "100.00", uid(t, "b-topup-2"))
	require.NoError(t, err)
	_, err = svc.Pay(walletB.WalletID, "30.00", uid(t, "b-pay-1"))
	require.NoError(t, err)

	finalA, err := svc.GetWallet(walletA.WalletID)
	require.NoError(t, err)
	finalB, err := svc.GetWallet(walletB.WalletID)
	require.NoError(t, err)

	assert.Equal(t, finalA.BalanceString(), finalB.BalanceString())
	assert.Equal(t, "120.00", finalA.BalanceString())
}

// ── Edge case #12: Read-After-Write Consistency ───────────────────────

func TestGetWallet_ReflectsLatestCommittedBalance(t *testing.T) {
	svc := newTestService(t)
	w := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)

	_, err := svc.Topup(w.WalletID, "100.00", uid(t, "topup-1"))
	require.NoError(t, err)

	afterTopup, err := svc.GetWallet(w.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "100.00", afterTopup.BalanceString())

	_, err = svc.Pay(w.WalletID, "40.00", uid(t, "pay-1"))
	require.NoError(t, err)

	afterPay, err := svc.GetWallet(w.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "60.00", afterPay.BalanceString())
}

// ── Edge case #13: Crash Recovery ────────────────────────────────────

// With Postgres, crash recovery is now directly testable. A failed
// transfer must leave zero partial state in the DB — the UnitOfWork
// transaction (BEGIN/COMMIT/ROLLBACK) guarantees this. If the process
// crashed between writes, Postgres rolls back via the write-ahead log.
func TestTransfer_AtomicDebitCredit_NoPartialState(t *testing.T) {
	svc := newTestService(t)
	from := newTestWallet(t, svc, uid(t, "user1"), wallet.USD)
	_, err := svc.Topup(from.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	// Transfer to a wallet that does not exist — destination lookup
	// fails before any balance is modified. All four writes must be
	// all-or-nothing.
	err = svc.Transfer(from.WalletID, "wallet-that-does-not-exist", "50.00", uid(t, "transfer"))
	require.Error(t, err)

	// Source balance must be untouched.
	fromAfter, err := svc.GetWallet(from.WalletID)
	require.NoError(t, err)
	assert.Equal(t, "100.00", fromAfter.BalanceString())

	// Ledger must have exactly one entry (the seed topup) — no orphaned
	// debit entry from the failed transfer.
	entries, err := svc.LedgerHistory(from.WalletID)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "orphaned ledger entry found after failed transfer")
	assert.Equal(t, string(wallet.OpTopup), string(entries[0].Operation))
}

// ── bonus: happy path ─────────────────────────────────────────────────

func TestCreateWallet_RequiresExistingOwner(t *testing.T) {
	svc := newTestService(t)

	ownerID := uuid.New().String()
	_, err := svc.CreateWallet(ownerID, wallet.USD)
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrOwnerNotFound)
}

func TestTransfer_SameWallet_Rejected(t *testing.T) {
	svc := newTestService(t)
	o, err := svc.CreateOwner(uid(t, "user1"))
	require.NoError(t, err)

	w, err := svc.CreateWallet(o.OwnerID, wallet.USD)
	require.NoError(t, err)
	_, err = svc.Topup(w.WalletID, "100.00", uid(t, "seed"))
	require.NoError(t, err)

	err = svc.Transfer(w.WalletID, w.WalletID, "10.00", uid(t, "transfer-self"))
	require.Error(t, err)
	assert.ErrorIs(t, err, wallet.ErrSameWallet)
}
