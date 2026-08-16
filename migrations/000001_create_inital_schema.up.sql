CREATE TABLE IF NOT EXISTS owners (
    owner_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS wallets (
    wallet_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id UUID NOT NULL,
    currency VARCHAR(3) NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT fk_wallets_owners
        FOREIGN KEY (owner_id)
        REFERENCES owners(owner_id)
        ON DELETE RESTRICT,
    CONSTRAINT uq_wallets_owner_currency UNIQUE (owner_id, currency)
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    entry_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id UUID NOT NULL,
    currency VARCHAR(3) NOT NULL,
    amount BIGINT NOT NULL DEFAULT 0,
    operation VARCHAR(20) NOT NULL,
    idempotency_key VARCHAR(100) NOT NULL,
    related_wallet UUID NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT fk_ledger_entries_wallets
        FOREIGN KEY (wallet_id)
        REFERENCES wallets(wallet_id)
        ON DELETE RESTRICT,
    CONSTRAINT fk_related_ledger_entries_wallets
        FOREIGN KEY (related_wallet)
        REFERENCES wallets(wallet_id)
        ON DELETE RESTRICT,
    CONSTRAINT uq_ledger_idempotency UNIQUE (idempotency_key)
);

CREATE OR REPLACE FUNCTION block_ledger_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'CRITICAL: Table ledger_entries is append-only. Operation % is forbidden.', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_block_ledger_mutation ON ledger_entries;
CREATE TRIGGER trg_block_ledger_mutation
BEFORE UPDATE OR DELETE ON ledger_entries
FOR EACH ROW
EXECUTE FUNCTION block_ledger_mutation();