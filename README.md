# E-Wallet System

A simplified multi-currency e-wallet backend — the core ledger for a
fintech app. Built in Go with Postgres for storage, Echo for the HTTP
layer, and Swagger for API documentation.

## Prerequisites

- Go 1.26+
- Docker & Docker Compose (for running the app, and for running the
  tests — they spin up a real Postgres via Testcontainers)

## Running it

```bash
# 1. Copy env file and adjust if needed
cp .env.example .env

# 2. Generate Swagger docs (only needed once, or after changing annotations)
go install github.com/swaggo/swag/cmd/swag@latest
swag init -g cmd/server/main.go -o docs

# 3. Download dependencies
go mod tidy

# 4. Start Postgres + app
docker compose up --build
```

The server starts on port `8080`. Swagger UI is at:
`http://localhost:8080/swagger/index.html`

To run the app without Docker (Postgres must already be running):

```bash
export DATABASE_URL=postgres://ewallet:ewallet@localhost:5432/ewallet?sslmode=disable
go run ./cmd/server
```

Migrations run automatically on startup — no manual steps needed.

## API endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/owners` | Register a new owner |
| `GET` | `/owners` | List all owners |
| `POST` | `/wallets` | Create a wallet |
| `GET` | `/wallets?owner_id=` | List wallets for an owner |
| `GET` | `/wallets/:id` | Get wallet status & balance |
| `POST` | `/wallets/:id/topup` | Add money |
| `POST` | `/wallets/:id/pay` | Deduct money |
| `POST` | `/wallets/:id/suspend` | Suspend a wallet |
| `POST` | `/wallets/transfer` | Transfer between wallets |
| `GET` | `/wallets/:id/ledger` | Full ledger history |

All mutating operations (`topup`, `pay`, `transfer`) require an
`Idempotency-Key` HTTP header (not a body field) — a second request with
the same key returns the same result without re-executing the
operation. See the Swagger UI for full request/response schemas.

Supported currencies: `usd`, `eur`, `idr` (`wallet.Currencies`). USD/EUR
use 2 decimal places; IDR uses 0 (not using cents due to daily transaction habits). Creating a wallet with any other currency code is rejected.

## Quick example

Mirrors the spec's Sample Usage script, translated to HTTP calls.
`jq` is used to pull IDs out of responses — swap in your own JSON
parsing if you don't have it installed.

```bash
BASE=http://localhost:8080

# 1. Register two owners
USER1=$(curl -s -X POST $BASE/owners -H 'Content-Type: application/json' \
  -d '{"name":"user1"}' | jq -r '.data.owner_id')
USER2=$(curl -s -X POST $BASE/owners -H 'Content-Type: application/json' \
  -d '{"name":"user2"}' | jq -r '.data.owner_id')

# 2. Create wallets — one per currency per user
W1_USD=$(curl -s -X POST $BASE/wallets -H 'Content-Type: application/json' \
  -d "{\"owner_id\":\"$USER1\",\"currency\":\"usd\"}" | jq -r '.data.wallet_id')
W1_EUR=$(curl -s -X POST $BASE/wallets -H 'Content-Type: application/json' \
  -d "{\"owner_id\":\"$USER1\",\"currency\":\"eur\"}" | jq -r '.data.wallet_id')
W2_USD=$(curl -s -X POST $BASE/wallets -H 'Content-Type: application/json' \
  -d "{\"owner_id\":\"$USER2\",\"currency\":\"usd\"}" | jq -r '.data.wallet_id')

# 3. Top-ups (Idempotency-Key required on every mutating call)
curl -s -X POST $BASE/wallets/$W1_USD/topup \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"amount":"1000.50"}'
curl -s -X POST $BASE/wallets/$W1_EUR/topup \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"amount":"500.25"}'
curl -s -X POST $BASE/wallets/$W2_USD/topup \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"amount":"200.75"}'

# 4. Payments
curl -s -X POST $BASE/wallets/$W1_USD/pay \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d '{"amount":"200.10"}'

# 5. Transfer — same currency only.
curl -s -X POST $BASE/wallets/transfer \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d "{\"from_wallet_id\":\"$W1_USD\",\"to_wallet_id\":\"$W2_USD\",\"amount\":\"300.40\"}"

# 5b. Currency mismatch is rejected (user1's EUR wallet → user2's USD
# wallet). This is the HTTP-API equivalent of the spec's sample comment
# ("should fail if user2 has no EUR wallet") — with UUID wallet IDs
# there's no "user2-EUR" address to even reference if it was never
# created, so the closest faithful check is the currency-mismatch guard
# that fires whenever from/to don't share a currency.
curl -s -X POST $BASE/wallets/transfer \
  -H 'Content-Type: application/json' -H "Idempotency-Key: $(uuidgen)" \
  -d "{\"from_wallet_id\":\"$W1_EUR\",\"to_wallet_id\":\"$W2_USD\",\"amount\":\"10.00\"}"
# → 400 Bad Request: "wallets have different currencies"

# 6. Query
curl -s $BASE/wallets/$W1_USD
```

`GET /wallets/{id}` response shape:

```json
{
  "success": true,
  "status": 200,
  "code": "SUCCESS",
  "message": "Wallet retrieved successfully",
  "data": {
    "wallet_id": "b2f1c3d4-...",
    "owner_id": "a1b2c3d4-...",
    "currency": "usd",
    "status": "active",
    "created_at": "2026-08-16T09:14:00Z",
    "balance": "500.00"
  },
  "meta": { "timestamp": "2026-08-16T09:30:00Z" }
}
```

`balance` and `status` come from `Wallet`'s custom `MarshalJSON` and
`json:"status"` tag respectively — internally `Balance` is an `int64` in
cents, never serialized as a raw number.

## Running the tests

Tests use [Testcontainers](https://testcontainers.com/) — a real
Postgres container is spun up automatically when the test suite starts,
runs all tests against it, then tears it down. Docker must be running.

```bash
go test -race -v ./...
```

`-race` isn't optional — some tests (concurrent spending, edge case #7)
are specifically checking for race conditions that don't always show up
on a plain run without the race detector.

Coverage:

```bash
go test -cover ./...
```

## Project layout

```
.
├── cmd/server/main.go           # entry point: Echo setup, migration, graceful shutdown
├── wallet/
│   ├── wallet.go                # domain model: Wallet, Owner, LedgerEntry
│   ├── owner.go                 # Owner struct
│   ├── ledger.go                # LedgerEntry struct
│   ├── repository.go            # interfaces: WalletRepository, LedgerRepository, OwnerRepository
│   ├── service.go               # all business logic lives here
│   └── service_test.go          # 13 edge cases + bonus tests (Testcontainers)
├── storage/postgres/postgres.go # Postgres implementation of the three interfaces
├── handler/handler.go           # Echo HTTP handlers + Swagger annotations
├── db/db.go                     # DB connection setup + migration runner
├── migrations/
│   ├── 000001_create_inital_schema.{up,down}.sql
│   └── 000002_add_wallet_owner_currency_unique.{up,down}.sql
├── docs/                        # generated by swag init — balance/amount/status
│                                 # fields hand-corrected afterward (swag can't see
│                                 # custom MarshalJSON), see comment at top of docs.go
├── docker-compose.yml
├── Dockerfile
└── .env.example
```

The business logic in `wallet/service.go` doesn't know or care how data
gets stored — it only talks to the interfaces in `repository.go`. The
Postgres implementation in `storage/postgres` satisfies those contracts;
swapping in a different backend (Redis, another SQL database) would only
require a new implementation of the same interfaces, with zero changes
to `service.go`.

## Design decisions & assumptions
### Money is stored as an integer, not a float or a uint

Balances are `int64`, in the currency's smallest unit (cents for USD,
for example) — not `float64` (binary floating point isn't a good fit
for money) and not `uint64` either. The reasoning on `uint64`: if a
subtraction bug ever produced a negative result, `uint64` would
silently underflow into some astronomically large number instead of
failing loudly — that's about the worst kind of bug you could have in a
financial system. `int64` gives you a negative number, which is
obviously wrong and easy to catch.

The actual "balance can't go negative" rule lives as an explicit check
in the service layer — it's not something baked into the type itself.

**On the spec's wording**: the assignment says *"no floating-point
arithmetic for money (use decimal, big.Float, or BigDecimal)"*. This
implementation uses none of those three named types — it's plain `int64`
arithmetic in the smallest unit instead. That's a deliberate choice, not
an oversight: the actual requirement is "no floating-point arithmetic",
and the three examples are suggestions, not the only valid answers.
`int64` satisfies the requirement more strictly than one of its own
suggestions does — `big.Float` is still binary floating point under the
hood, just with extra precision bolted on, so it carries the same class
of rounding risk the requirement is trying to rule out. Integer
arithmetic in the smallest unit has zero floating-point involved at any
step, and it's the same pattern real-world payment systems (Stripe, for
one) use for exactly this reason. `decimal`/`BigDecimal`-style types
make more sense in languages that don't have a safe native integer for
this (JavaScript's `Number` is a float; that's likely why the spec —
written for both Node.js and Go candidates — leads with those examples),
but Go's `int64` already does the job without pulling in an external
library.

### Smallest unit is per-currency, not one constant for everything

- USD, EUR: 2 decimal places (cents), per ISO 4217.
- IDR: implemented as **0 decimal places**. Officially ISO 4217 gives
  IDR 2 decimals (sen), but sen hasn't been used in any real Indonesian
  financial system in a long time — nothing below 1 Rupiah actually
  circulates. So IDR amounts round to the nearest Rupiah.

### Rounding: round half up

`12.345 → 12.35`, not banker's rounding (round half to even). Went with
half-up because it's more predictable and closer to what most people
expect from a basic financial transaction. The spec's own example
(`12.345 → 12.35`) is consistent with this choice.

### The ledger is the source of truth (double-entry style)

Every balance change goes through a ledger entry — append-only, never
edited or deleted afterward. `Wallet.Balance` is a cached value that
should always be reconcilable against `SUM(ledger_entries)` for that
wallet. Entries are signed: positive for credits (top-ups, incoming
transfers), negative for debits (payments, outgoing transfers) — so
summing them directly gives you the balance without needing to know what
type of operation each one was.

At the database level this is a structural invariant, not just an
application-level convention: a Postgres trigger
(`block_ledger_mutation`, see `migrations/000001_...up.sql`) raises an
exception on any `UPDATE` or `DELETE` against `ledger_entries`. Only
`INSERT` is ever allowed, even from outside the application.

### Owner is an explicit entity, not something created on the fly

You have to register an `Owner` via `POST /owners` before you can
`POST /wallets` for them (`ErrOwnerNotFound` → 404 otherwise). Kept
deliberately minimal — this isn't a user account/auth system, just an
identity for wallets to attach to. Making it explicit means "who's
registered" is actually visible and queryable, and it lets `Transfer`
check that two wallets really do belong to different owners — not just
that they have different wallet IDs.

### Idempotency keys are required for topup, pay, and transfer

Every mutating operation takes an `Idempotency-Key` HTTP header. A
second request with the same key gets back the same result without
re-executing the operation — that's what stops a retried request from
double-charging or double-crediting someone. The key is stored on the
ledger entry (`uq_ledger_idempotency` unique constraint), so duplicate
detection survives server restarts, not just process memory.

### Concurrency: per-wallet mutex + Postgres row-level lock

Two layers of concurrency protection:

1. **Per-wallet mutex in `Service`** — wraps the whole operation (read
   balance → compute → validate → write ledger → commit balance) so no
   other goroutine in the same process can sneak in halfway through.
   This stops lost updates and keeps concurrent spending from pushing a
   balance negative within a single instance.
2. **`SELECT ... FOR UPDATE` in `UpdateBalance`** — the Postgres
   implementation acquires a row-level lock before reading the balance,
   inside a transaction. This means even if two separate app instances
   try to update the same wallet concurrently, only one can hold the
   row lock at a time — the other blocks until the first commits. This
   is what makes the system safe across multiple instances, not just
   within one.

The `expectedBalance` check in `UpdateBalance` is kept as a second line
of defense, even though `SELECT FOR UPDATE` already prevents concurrent writes — it
catches any case where the row was somehow modified outside of the
application's own lock.

**Lock ordering on transfers**: the two wallet mutexes are always
acquired in order of `WalletID` (not source/destination order), which
prevents deadlock when two transfers going opposite directions (A→B and
B→A) run at the same time.

**Wallet creation has its own race, closed at the schema level**: two
concurrent `CreateWallet` calls for the same owner+currency can both
pass the application-level "does this already exist?" check before
either has inserted. A `UNIQUE (owner_id, currency)` constraint
(`migrations/000002_add_wallet_owner_currency_unique.up.sql`) closes
this at the database level — exactly one insert wins, the rest come
back as `ErrWalletAlreadyExists`. See
`TestCreateWallet_ConcurrentSameCurrency_OnlyOneSucceeds` for the full story.

### SQL injection protection

All queries use `$N` positional parameters — no string interpolation
into SQL anywhere in `storage/postgres`. `sqlx` passes parameters
separately from the query string, so the database driver handles
escaping at the protocol level.

### Cross-currency transfers are rejected, per the spec

Transferring between wallets with different currencies always fails
(`ErrCurrencyMismatch` → 400). Supporting it with a live exchange rate
was considered at one point, but dropped — it directly contradicts the
spec ("only allow transfers when currencies match"), and it would break
the ledger's current symmetry (debit and credit always being the exact
same amount).

### No authentication — a scope decision, not an oversight

The assignment doesn't mention auth. Adding JWT or API key auth would
be straightforward on top of Echo middleware, but it would also shift
time away from the correctness and concurrency work the spec actually
focuses on. All endpoints are open.

### Crash recovery

With Postgres as the backend, edge case #13 (crash recovery) is fully
testable. The Postgres `UpdateBalance` implementation wraps both the
debit and credit in a real database transaction (`BEGIN`/`COMMIT`/
`ROLLBACK`). `TestTransfer_AtomicDebitCredit_NoPartialState` verifies
that a failed transfer leaves zero orphaned ledger entries — the
database's write-ahead log and transaction rollback guarantee this,
not just application-level checks.

## Edge case coverage

| # | Edge case | Test |
|---|---|---|
| 1 | Decimal precision | `TestDecimal_Precision_Topup_RoundsHalfUp`, `TestDecimal_Precision_Payment_BelowSmallestUnit_Rejected` |
| 2 | Large balances | `TestLargeBalance_OneBillion_StoredSafely` |
| 3 | Currency mismatch | `TestTransfer_CurrencyMismatch_Rejected` |
| 4 | Multiple wallets per user | `TestCreateWallet_OneWalletPerCurrencyPerUser`, `TestCreateWallet_ConcurrentSameCurrency_OnlyOneSucceeds` |
| 5 | Zero/negative amounts | `TestTopup_ZeroAmount_Rejected`, `TestTopup_NegativeAmount_Rejected`, `TestPay_ZeroAmount_Rejected`, `TestPay_NegativeAmount_Rejected` |
| 6 | Duplicate requests | `TestTopup_DuplicateRequest_Idempotent`, `TestPay_DuplicateRequest_Idempotent`, `TestTransfer_DuplicateRequest_Idempotent` |
| 7 | Concurrent spending | `TestPay_ConcurrentSpending_NeverGoesNegative` (run with `-race`) |
| 8 | Partial failure during transfer | `TestTransfer_DestinationNotFound_SourceNotDebited`, `TestTransfer_InsufficientBalance_NothingCommitted` |
| 9 | Ledger vs balance mismatch | `TestLedger_SumMatchesBalance_AfterMultipleOperations` |
| 10 | Suspended wallet operations | `TestSuspendedWallet_Topup_Rejected`, `TestSuspendedWallet_Pay_Rejected`, `TestSuspendedWallet_TransferOut_Rejected`, `TestSuspendedWallet_TransferIn_Rejected` |
| 11 | Out-of-order requests | `TestOperations_ConsistentRegardlessOfOrder` |
| 12 | Read-after-write consistency | `TestGetWallet_ReflectsLatestCommittedBalance` |
| 13 | System restart / crash recovery | `TestTransfer_AtomicDebitCredit_NoPartialState` — verifies no orphaned ledger entries after a failed transfer, backed by Postgres transaction rollback |