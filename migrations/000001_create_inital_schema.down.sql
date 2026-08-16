DROP TRIGGER IF EXISTS trg_block_ledger_mutation ON ledger_entries;
DROP FUNCTION IF EXISTS block_ledger_mutation();

DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS wallets;
DROP TABLE IF EXISTS owners;