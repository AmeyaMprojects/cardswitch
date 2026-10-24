// Package issuer simulates a card-issuing bank: it holds per-token balances
// and answers authorization requests with ISO 8583-style response codes.
package issuer

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
)

// ISO 8583 response codes used by the simulator.
const (
	CodeApproved          = "00"
	CodeDoNotHonor        = "05"
	CodeInvalidAmount     = "13"
	CodeInsufficientFunds = "51"
)

type AuthRequest struct {
	ChargeID    string `json:"charge_id"`
	Token       string `json:"token"`
	AmountMinor int64  `json:"amount_minor"` // integer minor units (cents), never float
	Currency    string `json:"currency"`
}

type AuthResponse struct {
	ChargeID     string `json:"charge_id"`
	ResponseCode string `json:"response_code"`
	AuthCode     string `json:"auth_code,omitempty"`
}

// Account is one card's state at the issuer.
type Account struct {
	AvailableMinor int64
	Blocked        bool
}

// Issuer holds all accounts plus every auth it has answered, keyed by charge_id.
// mu guards both maps: HTTP handlers run concurrently, and Go maps are not
// safe for concurrent writes.
type Issuer struct {
	mu           sync.Mutex
	accounts     map[string]*Account     // token -> account
	auths        map[string]AuthResponse // charge_id -> response we gave
	defaultLimit int64                   // starting balance for unseen tokens
}

func New(defaultLimit int64) *Issuer {
	return &Issuer{
		accounts:     make(map[string]*Account),
		auths:        make(map[string]AuthResponse),
		defaultLimit: defaultLimit,
	}
}

// account returns the account for token, creating it with defaultLimit on
// first sight. The caller must hold i.mu.
func (i *Issuer) account(token string) *Account {
	acct, ok := i.accounts[token]
	if !ok {
		acct = &Account{AvailableMinor: i.defaultLimit}
		i.accounts[token] = acct
	}
	return acct
}

// Block marks a card as blocked (lost or stolen).
func (i *Issuer) Block(token string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.account(token).Blocked = true
}

// Available reports the remaining balance for a token.
func (i *Issuer) Available(token string) int64 {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.account(token).AvailableMinor
}

// Authorize decides one request. Rules, checked in this order:
//
//  1. Seen this charge_id before?  Return the stored response unchanged (idempotent replay).
//  2. amount <= 0                  "13" invalid amount
//  3. card blocked                 "05" do not honor
//  4. amount > available           "51" insufficient funds
//  5. otherwise                    "00" approved: subtract amount, attach 6-digit auth code
//
// Every response is stored under its charge_id. The lock covers the whole
// check-then-subtract so concurrent requests cannot overspend a balance.
func (i *Issuer) Authorize(req AuthRequest) AuthResponse {
	i.mu.Lock()
	defer i.mu.Unlock()

	if prev, ok := i.auths[req.ChargeID]; ok {
		return prev
	}

	resp := AuthResponse{ChargeID: req.ChargeID}
	acct := i.account(req.Token)
	switch {
	case req.AmountMinor <= 0:
		resp.ResponseCode = CodeInvalidAmount
	case acct.Blocked:
		resp.ResponseCode = CodeDoNotHonor
	case req.AmountMinor > acct.AvailableMinor:
		resp.ResponseCode = CodeInsufficientFunds
	default:
		acct.AvailableMinor -= req.AmountMinor
		resp.ResponseCode = CodeApproved
		resp.AuthCode = fmt.Sprintf("%06d", rand.IntN(1_000_000))
	}

	i.auths[req.ChargeID] = resp
	return resp
}

// Handler exposes Authorize over HTTP as POST /v1/authorize.
func (i *Issuer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		var req AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.ChargeID == "" || req.Token == "" {
			http.Error(w, "charge_id and token are required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(i.Authorize(req))
	})
	return mux
}
