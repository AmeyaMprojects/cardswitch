-- CardSwitch core schema

-- ---------- Tokenization vault ----------
CREATE TABLE vault_tokens (
    token        TEXT PRIMARY KEY,
    pan_hmac     BYTEA NOT NULL UNIQUE,   -- HMAC-SHA256(pan) for dedupe lookups
    pan_cipher   BYTEA NOT NULL,          -- AES-256-GCM: iv || ciphertext || tag
    bin          CHAR(6) NOT NULL,
    last4        CHAR(4) NOT NULL,
    key_version  INT NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- Idempotency ----------
CREATE TABLE idempotency_keys (
    merchant_id     TEXT NOT NULL,
    idem_key        TEXT NOT NULL,
    request_hash    BYTEA NOT NULL,       -- reject same key with different body
    response_status INT,
    response_body   JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (merchant_id, idem_key)
);

-- ---------- Charges ----------
CREATE TABLE charges (
    charge_id     UUID PRIMARY KEY,
    merchant_id   TEXT NOT NULL,
    token         TEXT NOT NULL REFERENCES vault_tokens(token),
    amount_minor  BIGINT NOT NULL CHECK (amount_minor > 0),
    currency      CHAR(3) NOT NULL,
    status        TEXT NOT NULL CHECK (status IN ('PENDING','APPROVED','DECLINED','REVERSED','CAPTURED','SETTLED')),
    issuer_id     TEXT,
    auth_code     TEXT,
    response_code TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------- Double-entry ledger ----------
CREATE TABLE accounts (
    account_id  TEXT PRIMARY KEY,         -- e.g. merchant:m_123:receivable, issuer:issuer-a:payable
    kind        TEXT NOT NULL CHECK (kind IN ('ASSET','LIABILITY','REVENUE','EXPENSE')),
    currency    CHAR(3) NOT NULL
);

CREATE TABLE ledger_transactions (
    txn_id          UUID PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE, -- duplicate postings become no-ops
    charge_id       UUID REFERENCES charges(charge_id),
    kind            TEXT NOT NULL CHECK (kind IN ('AUTH_HOLD','CAPTURE','REVERSAL','SETTLEMENT')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
    entry_id      BIGSERIAL PRIMARY KEY,
    txn_id        UUID NOT NULL REFERENCES ledger_transactions(txn_id),
    account_id    TEXT NOT NULL REFERENCES accounts(account_id),
    direction     TEXT NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
    amount_minor  BIGINT NOT NULL CHECK (amount_minor > 0)
);
CREATE INDEX ON ledger_entries (account_id);
CREATE INDEX ON ledger_entries (txn_id);

-- Enforce balanced transactions at commit time.
CREATE FUNCTION check_txn_balanced() RETURNS trigger AS $$
DECLARE diff BIGINT;
BEGIN
    SELECT COALESCE(SUM(CASE direction WHEN 'DEBIT' THEN amount_minor ELSE -amount_minor END), 0)
      INTO diff FROM ledger_entries WHERE txn_id = NEW.txn_id;
    IF diff <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % unbalanced by %', NEW.txn_id, diff;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER ledger_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_txn_balanced();

-- ---------- Settlement ----------
CREATE TABLE settlement_batches (
    batch_id     UUID PRIMARY KEY,
    merchant_id  TEXT NOT NULL,
    currency     CHAR(3) NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    window_end   TIMESTAMPTZ NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('OPEN','CLOSED','RECONCILED','MISMATCH')),
    total_minor  BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE settlement_lines (
    batch_id     UUID NOT NULL REFERENCES settlement_batches(batch_id),
    charge_id    UUID NOT NULL REFERENCES charges(charge_id),
    amount_minor BIGINT NOT NULL,
    PRIMARY KEY (batch_id, charge_id)
);
