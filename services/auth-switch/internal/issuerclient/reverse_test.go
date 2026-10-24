package issuerclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/breaker"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

func TestAuthorizeUnknownOutcomeIsSticky(t *testing.T) {
	tests := []struct {
		name   string
		script []int
	}{
		{"500 then 503s", []int{500, 503, 503}},
		{"500 then 400", []int{500, 400}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIssuer(t, 0, tc.script...)
			c, _ := newTestClient(testConfig())
			_, err := c.Authorize(context.Background(), f.route(), req)
			if !errors.Is(err, ErrUnknownOutcome) {
				t.Fatalf("err = %v, want ErrUnknownOutcome: attempt 1 may have been approved", err)
			}
		})
	}

	t.Run("breaker opens after an unknown outcome", func(t *testing.T) {
		f := newFakeIssuer(t, 0, 500)
		cfg := testConfig()
		cfg.BreakerThreshold = 1
		c, _ := newTestClient(cfg)
		_, err := c.Authorize(context.Background(), f.route(), req)
		if !errors.Is(err, ErrUnknownOutcome) || !errors.Is(err, breaker.ErrOpen) {
			t.Fatalf("err = %v, want both ErrUnknownOutcome and breaker.ErrOpen in the chain", err)
		}
	})

	t.Run("context ends after an unknown outcome", func(t *testing.T) {
		f := newFakeIssuer(t, 0, 500)
		cfg := testConfig()
		cfg.BaseBackoff, cfg.MaxBackoff = 5*time.Second, 5*time.Second
		c := New(cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := c.Authorize(ctx, f.route(), req)
		if !errors.Is(err, ErrUnknownOutcome) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want both ErrUnknownOutcome and context.DeadlineExceeded", err)
		}
	})

	t.Run("plain unavailable stays unavailable", func(t *testing.T) {
		f := newFakeIssuer(t, 0, 503)
		c, _ := newTestClient(testConfig())
		_, err := c.Authorize(context.Background(), f.route(), req)
		if errors.Is(err, ErrUnknownOutcome) {
			t.Fatalf("err = %v; every attempt was a clean 503, so nothing was processed", err)
		}
	})
}

// fakeReverser answers POST /v1/reverse with scripted status codes.
type fakeReverser struct {
	srv    *httptest.Server
	calls  atomic.Int32
	script []int
	got    atomic.Value // last charge_id received
}

func newFakeReverser(t *testing.T, script ...int) *fakeReverser {
	f := &fakeReverser{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1)) - 1
		if n >= len(f.script) {
			n = len(f.script) - 1
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/reverse" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var body reverseRequest
		json.NewDecoder(r.Body).Decode(&body)
		f.got.Store(body.ChargeID)
		if f.script[n] != http.StatusOK {
			w.WriteHeader(f.script[n])
			return
		}
		json.NewEncoder(w).Encode(reverseResponse{ChargeID: body.ChargeID, Result: "released"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeReverser) route() routing.Route {
	return routing.Route{IssuerID: "issuer-test", URL: f.srv.URL}
}

func TestReverse(t *testing.T) {
	t.Run("acknowledged first time", func(t *testing.T) {
		f := newFakeReverser(t, 200)
		c, slept := newTestClient(testConfig())
		if err := c.Reverse(context.Background(), f.route(), "c1"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if f.calls.Load() != 1 || len(*slept) != 0 {
			t.Fatalf("calls=%d sleeps=%d, want 1 and 0", f.calls.Load(), len(*slept))
		}
		if got := f.got.Load(); got != "c1" {
			t.Fatalf("issuer got charge_id %v, want c1", got)
		}
	})
	t.Run("keeps retrying past MaxRetries", func(t *testing.T) {
		f := newFakeReverser(t, 503, 500, 503, 500, 503, 200)
		c, slept := newTestClient(testConfig()) // MaxRetries is 2
		if err := c.Reverse(context.Background(), f.route(), "c1"); err != nil {
			t.Fatalf("err = %v", err)
		}
		if f.calls.Load() != 6 || len(*slept) != 5 {
			t.Fatalf("calls=%d sleeps=%d, want 6 and 5: a reversal must not give up after MaxRetries", f.calls.Load(), len(*slept))
		}
	})
	t.Run("stops on 4xx", func(t *testing.T) {
		f := newFakeReverser(t, 400)
		c, _ := newTestClient(testConfig())
		if err := c.Reverse(context.Background(), f.route(), "c1"); !errors.Is(err, ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
		if f.calls.Load() != 1 {
			t.Fatalf("calls = %d, want 1", f.calls.Load())
		}
	})
	t.Run("stops when context ends", func(t *testing.T) {
		f := newFakeReverser(t, 503)
		cfg := testConfig()
		cfg.BaseBackoff, cfg.MaxBackoff = 20*time.Millisecond, 20*time.Millisecond
		c := New(cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		err := c.Reverse(ctx, f.route(), "c1")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if f.calls.Load() < 2 {
			t.Fatalf("calls = %d, want several retries before the deadline", f.calls.Load())
		}
	})
	t.Run("ignores an open breaker", func(t *testing.T) {
		auth := newFakeIssuer(t, 0, 503)
		rev := newFakeReverser(t, 200)
		cfg := testConfig()
		cfg.MaxRetries, cfg.BreakerThreshold = 0, 1
		c, _ := newTestClient(cfg)
		route := routing.Route{IssuerID: "issuer-test", URL: auth.srv.URL}
		c.Authorize(context.Background(), route, req) // opens the breaker
		if _, err := c.Authorize(context.Background(), route, req); !errors.Is(err, breaker.ErrOpen) {
			t.Fatalf("setup: breaker should be open, got %v", err)
		}
		if err := c.Reverse(context.Background(), routing.Route{IssuerID: "issuer-test", URL: rev.srv.URL}, "c1"); err != nil {
			t.Fatalf("Reverse err = %v; reversals must bypass the breaker", err)
		}
	})
}
