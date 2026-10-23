// Package main simulates a card issuer. Behavior is controlled with env vars
// so failure scenarios (latency, errors, crashes) can be injected in tests.
package main

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	latencyMs, _ := strconv.Atoi(os.Getenv("ISSUER_LATENCY_MS"))
	failRate, _ := strconv.ParseFloat(os.Getenv("ISSUER_FAIL_RATE"), 64)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(latencyMs) * time.Millisecond)
		if rand.Float64() < failRate {
			http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
			return
		}
		// TODO: check balance/limits per token, return ISO 8583-style codes (00, 05, 51)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response_code":"00","auth_code":"123456"}`))
	})

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8082"
	}
	log.Printf("issuer-sim listening on %s (latency=%dms failRate=%.2f)", addr, latencyMs, failRate)
	log.Fatal(http.ListenAndServe(addr, mux))
}
