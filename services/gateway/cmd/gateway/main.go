// Package main is the CardSwitch API gateway: the merchant-facing entry point.
// It validates requests, enforces Idempotency-Key, swaps PANs for tokens via the
// vault, and forwards authorizations to the switch.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type ChargeRequest struct {
	MerchantID  string `json:"merchant_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	PAN         string `json:"pan,omitempty"`
	Token       string `json:"token,omitempty"`
	ExpiryMonth int    `json:"expiry_month"`
	ExpiryYear  int    `json:"expiry_year"`
}

type ChargeResponse struct {
	ChargeID     string `json:"charge_id"`
	Status       string `json:"status"` // APPROVED | DECLINED | REVERSED | PENDING
	ResponseCode string `json:"response_code"`
	Token        string `json:"token"`
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/charges", handleCharge)

	addr := getenv("LISTEN_ADDR", ":8080")
	log.Printf("gateway listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleCharge(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key header required", http.StatusBadRequest)
		return
	}
	var req ChargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// TODO: 1. Redis SETNX idempotency lock keyed on (merchant_id, key)
	// TODO: 2. tokenize PAN via tokenization-vault if raw PAN present
	// TODO: 3. forward to auth-switch with timeout + retry
	// TODO: 4. persist and cache response for replay on duplicate key
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	json.NewEncoder(w).Encode(ChargeResponse{Status: "PENDING", ResponseCode: "NI"})
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
