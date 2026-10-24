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

// Results of a reversal request.
const (
	ReverseReleased        = "released"         // an approved hold was found and the money given back
	ReverseNoHold          = "no_hold"          // nothing to release: declined, or never seen
	ReverseAlreadyReversed = "already_reversed" // a previous reversal already handled it
)

type ReverseRequest struct {
	ChargeID string `json:"charge_id"`
}

type ReverseResponse struct {
	ChargeID string `json:"charge_id"`
	Result   string `json:"result"`
}

// hold is the money an approval took, kept so a reversal can give it back.
type hold struct {
	token       string
	amountMinor int64
}

// Account is one card's state at the issuer.
type Account struct {
	AvailableMinor int64
	Blocked        bool
}

// Issuer holds all accounts plus every auth it has answered, keyed by charge_id.
// mu guards all maps: HTTP handlers run concurrently, and Go maps are not
// safe for concurrent writes.
type Issuer struct {
	mu           sync.Mutex
	accounts     map[string]*Account     // token -> account
	auths        map[string]AuthResponse // charge_id -> response we gave
	holds        map[string]hold         // charge_id -> approved amount not yet reversed
	reversed     map[string]bool         // charge_ids already reversed
	defaultLimit int64                   // starting balance for unseen tokens
}

func New(defaultLimit int64) *Issuer {
	return &Issuer{
		accounts:     make(map[string]*Account),
		auths:        make(map[string]AuthResponse),
		holds:        make(map[string]hold),
		reversed:     make(map[string]bool),
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
		i.holds[req.ChargeID] = hold{token: req.Token, amountMinor: req.AmountMinor}
	}

	i.auths[req.ChargeID] = resp
	return resp
}

// Reverse cancels the authorization for chargeID, if any. It is idempotent,
// and safe to call before the original authorization arrives: an unseen
// charge_id is stored as a decline, so a late authorization cannot take money.
func (i *Issuer) Reverse(chargeID string) ReverseResponse {
	i.mu.Lock()
	defer i.mu.Unlock()

	resp := ReverseResponse{ChargeID: chargeID}
	if i.reversed[chargeID] {
		resp.Result = ReverseAlreadyReversed
		return resp
	}
	i.reversed[chargeID] = true

	if h, ok := i.holds[chargeID]; ok {
		i.account(h.token).AvailableMinor += h.amountMinor
		delete(i.holds, chargeID)
		resp.Result = ReverseReleased
		return resp
	}
	if _, seen := i.auths[chargeID]; !seen {
		i.auths[chargeID] = AuthResponse{ChargeID: chargeID, ResponseCode: CodeDoNotHonor}
	}
	resp.Result = ReverseNoHold
	return resp
}

// Handler exposes the issuer over HTTP: POST /v1/authorize and POST /v1/reverse.
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
	mux.HandleFunc("POST /v1/reverse", func(w http.ResponseWriter, r *http.Request) {
		var req ReverseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ChargeID == "" {
			http.Error(w, "charge_id is required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(i.Reverse(req.ChargeID))
	})
	return mux
}
