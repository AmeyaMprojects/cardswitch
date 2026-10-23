// Package main is the authorization switch. It routes token-based auth
// requests to the right issuer by BIN range, wrapping each call with a
// timeout, bounded retries, and a per-issuer circuit breaker. On issuer
// timeout/failure after a hold is possibly placed, it emits a reversal.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

func main() {
	table := routing.DefaultTable()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/authorize", func(w http.ResponseWriter, r *http.Request) {
		// TODO: decode AuthRequest, resolve BIN -> issuer via table.Lookup,
		// call issuer through breaker.Do with context timeout, retry on
		// transient errors, publish reversal to Kafka on ambiguous failure,
		// write auth hold to ledger.
		_ = table
		w.WriteHeader(http.StatusNotImplemented)
	})

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	log.Printf("auth-switch listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
