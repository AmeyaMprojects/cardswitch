// Package main is the authorization switch. It routes token-based auth
// requests to the right issuer by BIN range, wrapping each call with a
// timeout, bounded retries, and a per-issuer circuit breaker. When an
// issuer's answer is lost, it reverses the charge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/events"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/issuerclient"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/reversal"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

// Response codes the switch itself returns when no issuer answer is available.
const (
	codeNoSuchIssuer      = "15"
	codeIssuerUnavailable = "91"
)

type authorizeRequest struct {
	issuerclient.AuthRequest
	MerchantID string `json:"merchant_id"`
	BIN        string `json:"bin"`
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
	publisher := events.NewKafkaPublisher(strings.Split(getenv("KAFKA_BOOTSTRAP", "localhost:9092"), ",")...)
	emitter := events.NewEmitter(publisher)
	reverser := reversal.New(client, emitter,
		envMillis("EVENT_PUBLISH_TIMEOUT_MS", 2000),
		envMillis("REVERSAL_TIMEOUT_MS", 5*60*1000))

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
		ev := events.AuthEvent{
			ChargeID:    req.ChargeID,
			MerchantID:  req.MerchantID,
			Token:       req.Token,
			IssuerID:    route.IssuerID,
			AmountMinor: req.AmountMinor,
			Currency:    req.Currency,
		}

		resp, err := client.Authorize(r.Context(), route, req.AuthRequest)
		if err != nil {
			log.Printf("charge %s to %s failed: %v", req.ChargeID, route.IssuerID, err)
			if errors.Is(err, issuerclient.ErrUnknownOutcome) {
				ev.Reason = events.ReasonIssuerTimeout
				if err := reverser.Reverse(ev, route); err != nil {
					log.Printf("charge %s: reversal event not published: %v", req.ChargeID, err)
				}
			}
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(issuerclient.AuthResponse{ChargeID: req.ChargeID, ResponseCode: codeIssuerUnavailable})
			return
		}

		if resp.ResponseCode == "00" {
			ev.ResponseCode, ev.AuthCode = resp.ResponseCode, resp.AuthCode
			if err := emitter.Approved(r.Context(), ev); err != nil {
				log.Printf("charge %s: approval event not published: %v", req.ChargeID, err)
			}
		}
		json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{Addr: getenv("LISTEN_ADDR", ":8081"), Handler: mux}
	go func() {
		log.Printf("auth-switch listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	// On SIGINT/SIGTERM: stop taking requests, let in-flight reversals finish,
	// then flush buffered Kafka messages.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	reverser.Wait()
	publisher.Close()
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
