// Package charges runs the merchant charge flow: idempotency, tokenization,
// authorization through the switch, and the ledger hold.
package charges

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusApproved = "APPROVED"
	StatusDeclined = "DECLINED"
	StatusPending  = "PENDING"

	codeApproved = "00"
)

type Request struct {
	MerchantID  string `json:"merchant_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	PAN         string `json:"pan,omitempty"`
	Token       string `json:"token,omitempty"`
	ExpiryMonth int    `json:"expiry_month,omitempty"`
	ExpiryYear  int    `json:"expiry_year,omitempty"`
}

type Response struct {
	ChargeID     string `json:"charge_id,omitempty"`
	Status       string `json:"status"`
	ResponseCode string `json:"response_code,omitempty"`
	Token        string `json:"token,omitempty"`
	Last4        string `json:"last4,omitempty"`
	AmountMinor  int64  `json:"amount_minor,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Result is what the HTTP layer sends back: a status code and a JSON body.
type Result struct {
	HTTPStatus int
	Body       []byte
	Replayed   bool
}

// Idempotency record as stored in Postgres.
type Record struct {
	RequestHash []byte
	Status      int // 0 while the first request is still running
	Body        []byte
}

type Locker interface {
	// Acquire returns a non-empty lock token if the lock was taken.
	Acquire(ctx context.Context, key string, ttl time.Duration) (string, error)
	Release(ctx context.Context, key, token string) error
}

type Store interface {
	// BeginIdempotent inserts a record for (merchant, key). created is false
	// if one already existed, in which case existing is returned.
	BeginIdempotent(ctx context.Context, merchantID, key string, hash []byte) (existing Record, created bool, err error)
	GetIdempotent(ctx context.Context, merchantID, key string) (rec Record, found bool, err error)
	CompleteIdempotent(ctx context.Context, merchantID, key string, status int, body []byte) error
	AbortIdempotent(ctx context.Context, merchantID, key string) error
	CreateCharge(ctx context.Context, c Charge) error
	UpdateCharge(ctx context.Context, c Charge) error
}

type Charge struct {
	ID           string
	MerchantID   string
	Token        string
	AmountMinor  int64
	Currency     string
	Status       string
	IssuerID     string
	ResponseCode string
	AuthCode     string
}

type TokenInfo struct {
	Token string `json:"token"`
	BIN   string `json:"bin"`
	Last4 string `json:"last4"`
}

type Vault interface {
	Tokenize(ctx context.Context, pan string) (TokenInfo, error)
	Lookup(ctx context.Context, token string) (TokenInfo, error)
}

type SwitchRequest struct {
	ChargeID    string `json:"charge_id"`
	MerchantID  string `json:"merchant_id"`
	Token       string `json:"token"`
	BIN         string `json:"bin"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type SwitchResponse struct {
	ChargeID     string `json:"charge_id"`
	ResponseCode string `json:"response_code"`
	AuthCode     string `json:"auth_code,omitempty"`
	IssuerID     string `json:"issuer_id,omitempty"`
}

type Switch interface {
	Authorize(ctx context.Context, req SwitchRequest) (SwitchResponse, error)
}

type Ledger interface {
	PostHold(ctx context.Context, c Charge) error
}

type Service struct {
	Locker  Locker
	Store   Store
	Vault   Vault
	Switch  Switch
	Ledger  Ledger
	LockTTL time.Duration
	NewID   func() string
}

func NewService(l Locker, s Store, v Vault, sw Switch, led Ledger) *Service {
	return &Service{Locker: l, Store: s, Vault: v, Switch: sw, Ledger: led, LockTTL: 30 * time.Second, NewID: uuid.NewString}
}

// Charge handles one POST /v1/charges.
//
// Duplicate protection has two layers. A Redis lock rejects concurrent
// requests with the same key cheaply. The Postgres record is the source of
// truth: it survives restarts and Redis outages, stores the response for
// replay, and detects a key reused with a different body.
func (s *Service) Charge(ctx context.Context, idemKey string, req Request) Result {
	if idemKey == "" {
		return errorResult(http.StatusBadRequest, "Idempotency-Key header required")
	}
	if msg := validate(req); msg != "" {
		return errorResult(http.StatusBadRequest, msg)
	}
	hash := requestHash(req)
	lockKey := "idem:" + req.MerchantID + ":" + idemKey

	lockToken, err := s.Locker.Acquire(ctx, lockKey, s.LockTTL)
	switch {
	case err != nil:
		// Redis is an optimisation; Postgres still guarantees correctness.
		log.Printf("idempotency lock unavailable, continuing on Postgres only: %v", err)
	case lockToken == "":
		// Another request holds the key. Replay its answer if it has one.
		existing, found, err := s.Store.GetIdempotent(ctx, req.MerchantID, idemKey)
		if err != nil {
			return errorResult(http.StatusServiceUnavailable, "idempotency store unavailable")
		}
		if !found {
			return errorResult(http.StatusConflict, "a request with this Idempotency-Key is still in progress")
		}
		return fromExisting(existing, hash)
	default:
		defer s.Locker.Release(context.WithoutCancel(ctx), lockKey, lockToken)
	}

	existing, created, err := s.Store.BeginIdempotent(ctx, req.MerchantID, idemKey, hash)
	if err != nil {
		return errorResult(http.StatusServiceUnavailable, "idempotency store unavailable")
	}
	if !created {
		return fromExisting(existing, hash)
	}

	res, retryable := s.process(ctx, req)
	if retryable {
		// Nothing reached the switch, so the client may safely retry with the same key.
		if err := s.Store.AbortIdempotent(context.WithoutCancel(ctx), req.MerchantID, idemKey); err != nil {
			log.Printf("abort idempotency record %s: %v", idemKey, err)
		}
		return res
	}
	if err := s.Store.CompleteIdempotent(context.WithoutCancel(ctx), req.MerchantID, idemKey, res.HTTPStatus, res.Body); err != nil {
		log.Printf("store idempotent response %s: %v", idemKey, err)
	}
	return res
}

// process runs the charge. retryable is true when it failed before the
// switch was called, so no money can have moved.
func (s *Service) process(ctx context.Context, req Request) (res Result, retryable bool) {
	var info TokenInfo
	var err error
	if req.PAN != "" {
		info, err = s.Vault.Tokenize(ctx, req.PAN)
	} else {
		info, err = s.Vault.Lookup(ctx, req.Token)
	}
	if err != nil {
		if errors.Is(err, ErrInvalidCard) {
			return errorResult(http.StatusBadRequest, err.Error()), true
		}
		return errorResult(http.StatusBadGateway, "tokenization failed"), true
	}

	c := Charge{
		ID:          s.NewID(),
		MerchantID:  req.MerchantID,
		Token:       info.Token,
		AmountMinor: req.AmountMinor,
		Currency:    req.Currency,
		Status:      StatusPending,
	}
	if err := s.Store.CreateCharge(ctx, c); err != nil {
		return errorResult(http.StatusServiceUnavailable, "charge store unavailable"), true
	}

	sw, err := s.Switch.Authorize(ctx, SwitchRequest{
		ChargeID:    c.ID,
		MerchantID:  c.MerchantID,
		Token:       c.Token,
		BIN:         info.BIN,
		AmountMinor: c.AmountMinor,
		Currency:    c.Currency,
	})
	if err != nil {
		// The switch may have authorized before we lost the reply. Leave the
		// charge PENDING and replay this answer; never re-send it.
		log.Printf("charge %s: switch call failed: %v", c.ID, err)
		return s.respond(http.StatusBadGateway, c, info), false
	}

	c.IssuerID, c.ResponseCode, c.AuthCode = sw.IssuerID, sw.ResponseCode, sw.AuthCode
	status := http.StatusPaymentRequired
	c.Status = StatusDeclined
	if sw.ResponseCode == codeApproved {
		c.Status = StatusApproved
		status = http.StatusOK
		if err := s.Ledger.PostHold(ctx, c); err != nil {
			log.Printf("charge %s: ledger hold failed: %v", c.ID, err)
		}
	}
	if err := s.Store.UpdateCharge(context.WithoutCancel(ctx), c); err != nil {
		log.Printf("charge %s: update failed: %v", c.ID, err)
	}
	return s.respond(status, c, info), false
}

func (s *Service) respond(status int, c Charge, info TokenInfo) Result {
	body, _ := json.Marshal(Response{
		ChargeID:     c.ID,
		Status:       c.Status,
		ResponseCode: c.ResponseCode,
		Token:        info.Token,
		Last4:        info.Last4,
		AmountMinor:  c.AmountMinor,
		Currency:     c.Currency,
	})
	return Result{HTTPStatus: status, Body: body}
}

func fromExisting(rec Record, hash []byte) Result {
	if string(rec.RequestHash) != string(hash) {
		return errorResult(http.StatusUnprocessableEntity, "Idempotency-Key reused with a different request body")
	}
	if rec.Status == 0 {
		return errorResult(http.StatusConflict, "a request with this Idempotency-Key is still in progress")
	}
	return Result{HTTPStatus: rec.Status, Body: rec.Body, Replayed: true}
}

// ErrInvalidCard is returned by a Vault for a PAN that fails validation.
var ErrInvalidCard = errors.New("invalid card number")

func validate(r Request) string {
	switch {
	case r.MerchantID == "":
		return "merchant_id is required"
	case r.AmountMinor <= 0:
		return "amount_minor must be positive"
	case len(r.Currency) != 3 || strings.ToUpper(r.Currency) != r.Currency:
		return "currency must be a 3-letter ISO code"
	case (r.PAN == "") == (r.Token == ""):
		return "exactly one of pan or token is required"
	}
	return ""
}

// requestHash fingerprints the request so a reused key with a different body
// is caught. Struct field order is fixed, so json.Marshal is deterministic.
func requestHash(r Request) []byte {
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return sum[:]
}

func errorResult(status int, msg string) Result {
	body, _ := json.Marshal(Response{Status: StatusDeclined, Error: msg})
	return Result{HTTPStatus: status, Body: body}
}
