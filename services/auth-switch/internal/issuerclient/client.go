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
	"net"
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
	// (could not connect, 503). Safe to retry.
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
		// Any failure while dialing (DNS lookup, refused, unreachable) means
		// the request was never sent.
		var opErr *net.OpError
		if errors.Is(err, syscall.ECONNREFUSED) || (errors.As(err, &opErr) && opErr.Op == "dial") {
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

// attempt makes one authorization call to the issuer, bounded by cfg.Timeout.
func (c *Client) attempt(ctx context.Context, baseURL string, req AuthRequest) (AuthResponse, error) {
	var out AuthResponse
	err := c.post(ctx, baseURL+"/v1/authorize", req, &out)
	return out, err
}

// post sends in as JSON to url and decodes the 200 reply into out. The call
// is bounded by cfg.Timeout and every failure is classified.
func (c *Client) post(ctx context.Context, url string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return classify(0, err)
	}
	defer resp.Body.Close()
	if err := classify(resp.StatusCode, nil); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return classify(0, err)
	}
	return nil
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
//
// If any attempt ended with an unknown outcome, the returned error matches
// ErrUnknownOutcome even when later attempts failed differently: the issuer
// may have approved that earlier attempt, so the caller must reverse.
func (c *Client) Authorize(ctx context.Context, route routing.Route, req AuthRequest) (AuthResponse, error) {
	unknown := false
	fail := func(err error) (AuthResponse, error) {
		if unknown && !errors.Is(err, ErrUnknownOutcome) {
			err = fmt.Errorf("%w: earlier attempt timed out: %w", ErrUnknownOutcome, err)
		}
		return AuthResponse{}, err
	}

	var lastErr error
	for n := 0; n <= c.cfg.MaxRetries; n++ {
		if n > 0 {
			if err := c.sleep(ctx, Backoff(n-1, c.cfg.BaseBackoff, c.cfg.MaxBackoff, c.jitter)); err != nil {
				return fail(err)
			}
		}
		resp, err := c.call(ctx, route, req)
		if err == nil {
			return resp, nil
		}
		if errors.Is(err, ErrUnknownOutcome) {
			unknown = true
		}
		if !retryable(err) {
			return fail(err)
		}
		lastErr = err
	}
	return fail(fmt.Errorf("after %d attempts: %w", c.cfg.MaxRetries+1, lastErr))
}

type reverseRequest struct {
	ChargeID string `json:"charge_id"`
}

type reverseResponse struct {
	ChargeID string `json:"charge_id"`
	Result   string `json:"result"`
}

// Reverse tells the issuer to cancel chargeID, retrying until the issuer
// acknowledges or ctx ends. It deliberately skips the circuit breaker: an
// open breaker means "stop sending new charges", but a reversal releases a
// cardholder's money and must keep trying.
func (c *Client) Reverse(ctx context.Context, route routing.Route, chargeID string) error {
	for n := 0; ; n++ {
		if n > 0 {
			if err := c.sleep(ctx, Backoff(n-1, c.cfg.BaseBackoff, c.cfg.MaxBackoff, c.jitter)); err != nil {
				return fmt.Errorf("reversal of %s not acknowledged after %d attempts: %w", chargeID, n, err)
			}
		}
		var out reverseResponse
		err := c.post(ctx, route.URL+"/v1/reverse", reverseRequest{ChargeID: chargeID}, &out)
		if err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
	}
}
