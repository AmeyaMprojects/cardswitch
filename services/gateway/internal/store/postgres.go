// Package store persists idempotency records and charges in Postgres.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AmeyaMprojects/cardswitch/services/gateway/internal/charges"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) BeginIdempotent(ctx context.Context, merchantID, key string, hash []byte) (charges.Record, bool, error) {
	tag, err := p.pool.Exec(ctx,
		`INSERT INTO idempotency_keys (merchant_id, idem_key, request_hash) VALUES ($1, $2, $3)
		 ON CONFLICT (merchant_id, idem_key) DO NOTHING`, merchantID, key, hash)
	if err != nil {
		return charges.Record{}, false, err
	}
	if tag.RowsAffected() == 1 {
		return charges.Record{}, true, nil
	}
	rec, _, err := p.GetIdempotent(ctx, merchantID, key)
	return rec, false, err
}

func (p *Postgres) GetIdempotent(ctx context.Context, merchantID, key string) (charges.Record, bool, error) {
	var rec charges.Record
	var status *int
	err := p.pool.QueryRow(ctx,
		`SELECT request_hash, response_status, response_body FROM idempotency_keys
		 WHERE merchant_id = $1 AND idem_key = $2`, merchantID, key).Scan(&rec.RequestHash, &status, &rec.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return charges.Record{}, false, nil
	}
	if err != nil {
		return charges.Record{}, false, err
	}
	if status != nil {
		rec.Status = *status
	}
	return rec, true, nil
}

func (p *Postgres) CompleteIdempotent(ctx context.Context, merchantID, key string, status int, body []byte) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE idempotency_keys SET response_status = $3, response_body = $4
		 WHERE merchant_id = $1 AND idem_key = $2`, merchantID, key, status, body)
	return err
}

func (p *Postgres) AbortIdempotent(ctx context.Context, merchantID, key string) error {
	_, err := p.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE merchant_id = $1 AND idem_key = $2 AND response_status IS NULL`, merchantID, key)
	return err
}

func (p *Postgres) CreateCharge(ctx context.Context, c charges.Charge) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO charges (charge_id, merchant_id, token, amount_minor, currency, status)
		 VALUES ($1, $2, $3, $4, $5, $6)`, c.ID, c.MerchantID, c.Token, c.AmountMinor, c.Currency, c.Status)
	return err
}

func (p *Postgres) UpdateCharge(ctx context.Context, c charges.Charge) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE charges SET status = $2, issuer_id = NULLIF($3, ''), response_code = $4,
		        auth_code = NULLIF($5, ''), updated_at = now()
		 WHERE charge_id = $1`, c.ID, c.Status, c.IssuerID, c.ResponseCode, c.AuthCode)
	return err
}
