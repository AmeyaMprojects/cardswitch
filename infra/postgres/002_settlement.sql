-- One open batch per merchant, currency and window; a charge settles once.
CREATE UNIQUE INDEX settlement_batches_window ON settlement_batches (merchant_id, currency, window_start);
CREATE UNIQUE INDEX settlement_lines_charge ON settlement_lines (charge_id);

ALTER TABLE settlement_batches
    ADD COLUMN closed_at       TIMESTAMPTZ,
    ADD COLUMN ledger_txn_id   UUID,
    ADD COLUMN reconciliation  JSONB;
