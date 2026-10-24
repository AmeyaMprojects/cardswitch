// Package main runs the issuer simulator. Behavior is controlled with env vars
// so failure scenarios (latency, errors, crashes) can be injected in tests.
package main

import (
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/issuer-sim/internal/issuer"
)

func main() {
	latencyMs, _ := strconv.Atoi(os.Getenv("ISSUER_LATENCY_MS"))
	failRate, _ := strconv.ParseFloat(os.Getenv("ISSUER_FAIL_RATE"), 64)
	limit, err := strconv.ParseInt(getenv("ISSUER_DEFAULT_LIMIT_MINOR", "100000"), 10, 64)
	if err != nil {
		log.Fatalf("bad ISSUER_DEFAULT_LIMIT_MINOR: %v", err)
	}

	iss := issuer.New(limit)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("/v1/", faults(iss.Handler(), time.Duration(latencyMs)*time.Millisecond, failRate))

	addr := getenv("LISTEN_ADDR", ":8082")
	log.Printf("issuer-sim listening on %s (latency=%dms failRate=%.2f limit=%d)", addr, latencyMs, failRate, limit)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// faults wraps a handler with injected latency and random 503s for chaos testing.
func faults(next http.Handler, latency time.Duration, failRate float64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(latency)
		if rand.Float64() < failRate {
			http.Error(w, "issuer unavailable", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
