// Package issuerclient calls issuers on behalf of the switch. Every call gets
// a per-attempt timeout, bounded retries with jittered exponential backoff,
// and goes through a per-issuer circuit breaker.
package issuerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/breaker"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

// Failure classes. Callers test for them with errors.Is.
var (
	// ErrUnavailable: the issuer certainly did not process the request
	// (connection refused, 503). Safe to retry.
	ErrUnavailable = errors.New("issuer unavailable")
	// ErrUnknownOutcome: the request may have reached the issuer and been
	// processed (timeout, connection reset, 5xx). Retrying is safe only
	// because the issuer dedupes on charge_id.
	ErrUnknownOutcome = errors.New("issuer outcome unknown")
	// ErrRejected: the issuer refused the request itself (4xx). Retrying
	// the same bytes cannot help.
	ErrRejected = errors.New("issuer rejected request")
)

type AuthRequest struct {
	ChargeID    string `json:"charge_id"`
	Token       string `json:"token"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

type AuthResponse struct {
	ChargeID     string `json:"charge_id"`
	ResponseCode string `json:"response_code"`
	AuthCode     string `json:"auth_code,omitempty"`
}

type Config struct {
	Timeout          time.Duration // per attempt
	MaxRetries       int           // extra attempts after the first
	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
	BreakerThreshold int           // consecutive failures before opening
	BreakerCooldown  time.Duration // how long it stays open
}

type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	breakers map[string]*breaker.Breaker // issuer ID -> breaker

	// Swappable in tests so backoff doesn't slow them down.
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func() float64
}

func New(cfg Config) *Client {
	return &Client{
		cfg:      cfg,
		http:     &http.Client{},
		breakers: make(map[string]*breaker.Breaker),
		sleep:    sleepCtx,
		jitter:   rand.Float64,
	}
}

// sleepCtx waits for d, or returns early with ctx's error if ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Backoff returns the wait before retry number attempt (0-based), using
// "full jitter": a random duration in [0, min(max, base*2^attempt)).
func Backoff(attempt int, base, max time.Duration, jitter func() float64) time.Duration {
	d := base << attempt
	if d > max || d <= 0 {
		d = max
	}
	return time.Duration(jitter() * float64(d))
}

// classify turns the result of one HTTP exchange into nil or a failure class.
func classify(status int, err error) error {
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return fmt.Errorf("%w: %w", ErrUnknownOutcome, err)
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusServiceUnavailable:
		return fmt.Errorf("%w: status %d", ErrUnavailable, status)
	case status >= 500:
		return fmt.Errorf("%w: status %d", ErrUnknownOutcome, status)
	default:
		return fmt.Errorf("%w: status %d", ErrRejected, status)
	}
}

// retryable reports whether another attempt could succeed.
func retryable(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnknownOutcome)
}

// attempt makes one HTTP call to the issuer, bounded by cfg.Timeout.
func (c *Client) attempt(ctx context.Context, baseURL string, req AuthRequest) (AuthResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(req)
	if err != nil {
		return AuthResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/authorize", bytes.NewReader(body))
	if err != nil {
		return AuthResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return AuthResponse{}, classify(0, err)
	}
	defer resp.Body.Close()
	if err := classify(resp.StatusCode, nil); err != nil {
		return AuthResponse{}, err
	}

	var out AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AuthResponse{}, classify(0, err)
	}
	return out, nil
}

// breakerFor returns the issuer's breaker, creating it on first use.
func (c *Client) breakerFor(issuerID string) *breaker.Breaker {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.breakers[issuerID]
	if !ok {
		b = breaker.New(c.cfg.BreakerThreshold, c.cfg.BreakerCooldown)
		c.breakers[issuerID] = b
	}
	return b
}

// call runs one attempt through the issuer's breaker. Only issuer-health
// failures count against the breaker; a rejection is our fault, not theirs.
func (c *Client) call(ctx context.Context, route routing.Route, req AuthRequest) (AuthResponse, error) {
	var resp AuthResponse
	var callErr error
	brErr := c.breakerFor(route.IssuerID).Do(func() error {
		resp, callErr = c.attempt(ctx, route.URL, req)
		if retryable(callErr) {
			return callErr
		}
		return nil
	})
	if brErr != nil {
		return AuthResponse{}, brErr
	}
	return resp, callErr
}

// Authorize sends req to the routed issuer, retrying transient failures up
// to cfg.MaxRetries times with backoff between attempts.
func (c *Client) Authorize(ctx context.Context, route routing.Route, req AuthRequest) (AuthResponse, error) {
	var lastErr error
	for n := 0; n <= c.cfg.MaxRetries; n++ {
		if n > 0 {
			if err := c.sleep(ctx, Backoff(n-1, c.cfg.BaseBackoff, c.cfg.MaxBackoff, c.jitter)); err != nil {
				return AuthResponse{}, err
			}
		}
		resp, err := c.call(ctx, route, req)
		if err == nil {
			return resp, nil
		}
		if !retryable(err) {
			return AuthResponse{}, err
		}
		lastErr = err
	}
	return AuthResponse{}, fmt.Errorf("after %d attempts: %w", c.cfg.MaxRetries+1, lastErr)
}
