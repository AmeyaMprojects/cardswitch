// Package main is the CardSwitch API gateway: the merchant-facing entry point.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/gateway/internal/charges"
	"github.com/AmeyaMprojects/cardswitch/services/gateway/internal/clients"
	"github.com/AmeyaMprojects/cardswitch/services/gateway/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pg, err := store.NewPostgres(ctx, getenv("DATABASE_URL", "postgres://cardswitch:cardswitch@localhost:5432/cardswitch"))
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer pg.Close()
	locker := store.NewRedisLocker(getenv("REDIS_ADDR", "localhost:6379"))
	defer locker.Close()

	svc := charges.NewService(locker, pg,
		clients.NewVault(getenv("VAULT_URL", "http://localhost:8083")),
		clients.NewSwitch(getenv("SWITCH_URL", "http://localhost:8081")),
		clients.NewLedger(getenv("LEDGER_URL", "http://localhost:8084")))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("POST /v1/charges", func(w http.ResponseWriter, r *http.Request) {
		var req charges.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
			return
		}
		res := svc.Charge(r.Context(), r.Header.Get("Idempotency-Key"), req)
		w.Header().Set("Content-Type", "application/json")
		if res.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		w.WriteHeader(res.HTTPStatus)
		w.Write(res.Body)
	})

	srv := &http.Server{Addr: getenv("LISTEN_ADDR", ":8080"), Handler: mux}
	go func() {
		log.Printf("gateway listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
