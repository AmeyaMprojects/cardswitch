// Package main is the authorization switch. It routes token-based auth
// requests to the right issuer by BIN range, wrapping each call with a
// timeout, bounded retries, and a per-issuer circuit breaker.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/issuerclient"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

// Response codes the switch itself returns when no issuer answer is available.
const (
	codeNoSuchIssuer      = "15"
	codeIssuerUnavailable = "91"
)

type authorizeRequest struct {
	issuerclient.AuthRequest
	BIN string `json:"bin"`
}

func main() {
	table := routing.DefaultTable()
	client := issuerclient.New(issuerclient.Config{
		Timeout:          envMillis("ISSUER_TIMEOUT_MS", 800),
		MaxRetries:       envInt("ISSUER_MAX_RETRIES", 2),
		BaseBackoff:      envMillis("ISSUER_BASE_BACKOFF_MS", 50),
		MaxBackoff:       envMillis("ISSUER_MAX_BACKOFF_MS", 500),
		BreakerThreshold: envInt("BREAKER_THRESHOLD", 5),
		BreakerCooldown:  envMillis("BREAKER_COOLDOWN_MS", 5000),
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		var req authorizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		route, ok := table.Lookup(req.BIN)
		if !ok {
			json.NewEncoder(w).Encode(issuerclient.AuthResponse{ChargeID: req.ChargeID, ResponseCode: codeNoSuchIssuer})
			return
		}

		resp, err := client.Authorize(r.Context(), route, req.AuthRequest)
		if err != nil {
			log.Printf("charge %s to %s failed: %v", req.ChargeID, route.IssuerID, err)
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(issuerclient.AuthResponse{ChargeID: req.ChargeID, ResponseCode: codeIssuerUnavailable})
			return
		}
		json.NewEncoder(w).Encode(resp)
	})

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	log.Printf("auth-switch listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envMillis(key string, def int) time.Duration {
	return time.Duration(envInt(key, def)) * time.Millisecond
}
