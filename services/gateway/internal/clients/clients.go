// Package clients calls the vault, switch and ledger over HTTP.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/gateway/internal/charges"
)

type httpJSON struct {
	base string
	http *http.Client
	// decode502 treats a 502 with a JSON body as an answer, not an error.
	decode502 bool
}

// do sends in (if non-nil) as JSON and decodes a 2xx reply into out.
// A non-2xx reply comes back as *StatusError.
func (h httpJSON) do(ctx context.Context, method, path string, in, out any) error {
	var b []byte
	if in != nil {
		var err error
		if b, err = json.Marshal(in); err != nil {
			return err
		}
	}
	body := bytes.NewReader(b)
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && !(h.decode502 && resp.StatusCode == http.StatusBadGateway && out != nil) {
		return &StatusError{Code: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("unexpected status %d", e.Code) }

type Vault struct{ h httpJSON }

func NewVault(base string) *Vault {
	return &Vault{httpJSON{base: base, http: &http.Client{Timeout: 2 * time.Second}}}
}

func (v *Vault) Tokenize(ctx context.Context, pan string) (charges.TokenInfo, error) {
	var out charges.TokenInfo
	err := v.h.do(ctx, http.MethodPost, "/v1/tokens", map[string]string{"pan": pan}, &out)
	if se, ok := err.(*StatusError); ok && se.Code == http.StatusUnprocessableEntity {
		return out, charges.ErrInvalidCard
	}
	return out, err
}

func (v *Vault) Lookup(ctx context.Context, token string) (charges.TokenInfo, error) {
	var out charges.TokenInfo
	err := v.h.do(ctx, http.MethodGet, "/v1/tokens/"+token, nil, &out)
	if se, ok := err.(*StatusError); ok && se.Code == http.StatusNotFound {
		return out, charges.ErrInvalidCard
	}
	return out, err
}

// Switch calls the authorization switch. A 502 from the switch still carries
// a JSON decline (code 91), so it is decoded rather than treated as an error.
type Switch struct{ h httpJSON }

func NewSwitch(base string) *Switch {
	// The switch bounds its own issuer retries; this only catches a hung switch.
	return &Switch{httpJSON{base: base, http: &http.Client{Timeout: 10 * time.Second}, decode502: true}}
}

func (s *Switch) Authorize(ctx context.Context, req charges.SwitchRequest) (charges.SwitchResponse, error) {
	var out charges.SwitchResponse
	err := s.h.do(ctx, http.MethodPost, "/v1/authorize", req, &out)
	return out, err
}

type Ledger struct{ h httpJSON }

func NewLedger(base string) *Ledger {
	return &Ledger{httpJSON{base: base, http: &http.Client{Timeout: 2 * time.Second}}}
}

type entry struct {
	Account     string `json:"account"`
	Direction   string `json:"direction"`
	AmountMinor int64  `json:"amount_minor"`
}

type posting struct {
	IdempotencyKey string  `json:"idempotency_key"`
	ChargeID       string  `json:"charge_id"`
	Kind           string  `json:"kind"`
	Currency       string  `json:"currency"`
	Entries        []entry `json:"entries"`
}

// PostHold records the authorization hold: the merchant is now owed the
// amount (receivable) and the issuer owes it (payable). Retried a few times;
// the ledger dedupes on the idempotency key, so retries are safe.
func (l *Ledger) PostHold(ctx context.Context, c charges.Charge) error {
	p := posting{
		IdempotencyKey: "hold:" + c.ID,
		ChargeID:       c.ID,
		Kind:           "AUTH_HOLD",
		Currency:       c.Currency,
		Entries: []entry{
			{Account: "merchant:" + c.MerchantID + ":receivable", Direction: "DEBIT", AmountMinor: c.AmountMinor},
			{Account: "issuer:" + c.IssuerID + ":payable", Direction: "CREDIT", AmountMinor: c.AmountMinor},
		},
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		if err = l.h.do(ctx, http.MethodPost, "/v1/postings", p, nil); err == nil {
			return nil
		}
	}
	return err
}
