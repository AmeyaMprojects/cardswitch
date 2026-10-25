package issuerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/breaker"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

func TestBackoff(t *testing.T) {
	base, max := 100*time.Millisecond, time.Second
	full := func() float64 { return 1.0 }
	half := func() float64 { return 0.5 }
	tests := []struct {
		attempt int
		jitter  func() float64
		want    time.Duration
	}{
		{0, full, 100 * time.Millisecond},
		{1, full, 200 * time.Millisecond},
		{2, full, 400 * time.Millisecond},
		{3, full, 800 * time.Millisecond},
		{4, full, time.Second},  // 1.6s capped at max
		{60, full, time.Second}, // shift overflows; must still cap, not go negative
		{2, half, 200 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempt=%d", tc.attempt), func(t *testing.T) {
			if got := Backoff(tc.attempt, base, max, tc.jitter); got != tc.want {
				t.Fatalf("Backoff = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	refused := fmt.Errorf("dial tcp: %w", syscall.ECONNREFUSED)
	tests := []struct {
		name   string
		status int
		err    error
		want   error // nil means success
	}{
		{"200 ok", 200, nil, nil},
		{"503 unavailable", 503, nil, ErrUnavailable},
		{"500 unknown", 500, nil, ErrUnknownOutcome},
		{"504 unknown", 504, nil, ErrUnknownOutcome},
		{"400 rejected", 400, nil, ErrRejected},
		{"404 rejected", 404, nil, ErrRejected},
		{"connection refused", 0, refused, ErrUnavailable},
		{"dns failure", 0, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "issuer-sim", IsNotFound: true}}, ErrUnavailable},
		{"reset after send", 0, &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, ErrUnknownOutcome},
		{"timeout", 0, context.DeadlineExceeded, ErrUnknownOutcome},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.status, tc.err)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("classify = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("classify = %v, want errors.Is(_, %v)", got, tc.want)
			}
			if tc.err != nil && !errors.Is(got, tc.err) {
				t.Fatalf("classify must wrap the original error too (use %%w twice), got %v", got)
			}
		})
	}
}

// fakeIssuer serves a scripted sequence of status codes and counts calls.
// After the script runs out it repeats the last entry.
type fakeIssuer struct {
	srv    *httptest.Server
	calls  atomic.Int32
	script []int
	delay  time.Duration
}

func newFakeIssuer(t *testing.T, delay time.Duration, script ...int) *fakeIssuer {
	f := &fakeIssuer{script: script, delay: delay}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1)) - 1
		if n >= len(f.script) {
			n = len(f.script) - 1
		}
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authorize" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if f.script[n] != http.StatusOK {
			w.WriteHeader(f.script[n])
			return
		}
		var req AuthRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(AuthResponse{ChargeID: req.ChargeID, ResponseCode: "00", AuthCode: "123456"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) route() routing.Route {
	return routing.Route{IssuerID: "issuer-test", URL: f.srv.URL}
}

func testConfig() Config {
	return Config{
		Timeout:          200 * time.Millisecond,
		MaxRetries:       2,
		BaseBackoff:      10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
		BreakerThreshold: 100, // high, so retry tests don't trip it
		BreakerCooldown:  time.Second,
	}
}

// newTestClient records requested sleeps instead of sleeping.
func newTestClient(cfg Config) (*Client, *[]time.Duration) {
	c := New(cfg)
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	c.jitter = func() float64 { return 1.0 }
	return c, &slept
}

var req = AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 1999, Currency: "USD"}

func TestAttempt(t *testing.T) {
	t.Run("decodes approval", func(t *testing.T) {
		f := newFakeIssuer(t, 0, 200)
		c, _ := newTestClient(testConfig())
		resp, err := c.attempt(context.Background(), f.srv.URL, req)
		if err != nil {
			t.Fatalf("attempt error: %v", err)
		}
		if resp.ChargeID != "c1" || resp.ResponseCode != "00" {
			t.Fatalf("got %+v", resp)
		}
	})
	t.Run("times out slow issuer", func(t *testing.T) {
		f := newFakeIssuer(t, time.Second, 200)
		cfg := testConfig()
		cfg.Timeout = 30 * time.Millisecond
		c, _ := newTestClient(cfg)
		start := time.Now()
		_, err := c.attempt(context.Background(), f.srv.URL, req)
		if !errors.Is(err, ErrUnknownOutcome) {
			t.Fatalf("err = %v, want ErrUnknownOutcome", err)
		}
		if el := time.Since(start); el > 500*time.Millisecond {
			t.Fatalf("attempt took %v; the per-attempt timeout isn't applied", el)
		}
	})
	t.Run("connection refused is unavailable", func(t *testing.T) {
		f := newFakeIssuer(t, 0, 200)
		url := f.srv.URL
		f.srv.Close()
		c, _ := newTestClient(testConfig())
		if _, err := c.attempt(context.Background(), url, req); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v, want ErrUnavailable", err)
		}
	})
	t.Run("4xx is rejected", func(t *testing.T) {
		f := newFakeIssuer(t, 0, 400)
		c, _ := newTestClient(testConfig())
		if _, err := c.attempt(context.Background(), f.srv.URL, req); !errors.Is(err, ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	})
}

func TestBreakerFor(t *testing.T) {
	c, _ := newTestClient(testConfig())
	a1, a2, b := c.breakerFor("a"), c.breakerFor("a"), c.breakerFor("b")
	if a1 == nil || a1 != a2 {
		t.Fatal("breakerFor must return the same breaker for the same issuer")
	}
	if a1 == b {
		t.Fatal("each issuer needs its own breaker")
	}
}

func TestAuthorizeRetries(t *testing.T) {
	tests := []struct {
		name      string
		script    []int
		wantErr   error
		wantCalls int32
		wantSleep int
	}{
		{"first try", []int{200}, nil, 1, 0},
		{"recovers after two 503s", []int{503, 503, 200}, nil, 3, 2},
		{"recovers after 500", []int{500, 200}, nil, 2, 1},
		{"gives up after max retries", []int{503}, ErrUnavailable, 3, 2},
		{"no retry on 4xx", []int{400}, ErrRejected, 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIssuer(t, 0, tc.script...)
			c, slept := newTestClient(testConfig())
			resp, err := c.Authorize(context.Background(), f.route(), req)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("err = %v, want success", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && resp.ResponseCode != "00" {
				t.Fatalf("resp = %+v, want approval", resp)
			}
			if got := f.calls.Load(); got != tc.wantCalls {
				t.Fatalf("issuer called %d times, want %d", got, tc.wantCalls)
			}
			if len(*slept) != tc.wantSleep {
				t.Fatalf("slept %d times, want %d (sleep before each retry, never before the first try)", len(*slept), tc.wantSleep)
			}
		})
	}
}

func TestAuthorizeBacksOffExponentially(t *testing.T) {
	f := newFakeIssuer(t, 0, 503)
	cfg := testConfig()
	cfg.MaxRetries = 4
	c, slept := newTestClient(cfg)
	c.Authorize(context.Background(), f.route(), req)
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 50 * time.Millisecond}
	if fmt.Sprint(*slept) != fmt.Sprint(want) {
		t.Fatalf("sleeps = %v, want %v", *slept, want)
	}
}

func TestAuthorizeStopsWhenContextCanceled(t *testing.T) {
	f := newFakeIssuer(t, 0, 503)
	cfg := testConfig()
	cfg.BaseBackoff, cfg.MaxBackoff = 5*time.Second, 5*time.Second
	c := New(cfg) // real sleep
	c.jitter = func() float64 { return 1.0 }

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Authorize(ctx, f.route(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("took %v; backoff must wake up when ctx ends", el)
	}
}

func TestBreakerOpensOnIssuerFailures(t *testing.T) {
	f := newFakeIssuer(t, 0, 503)
	cfg := testConfig()
	cfg.MaxRetries = 0
	cfg.BreakerThreshold = 3
	c, _ := newTestClient(cfg)
	for n := 0; n < 3; n++ {
		c.Authorize(context.Background(), f.route(), req)
	}
	_, err := c.Authorize(context.Background(), f.route(), req)
	if !errors.Is(err, breaker.ErrOpen) {
		t.Fatalf("err = %v, want breaker.ErrOpen after 3 failures", err)
	}
	if got := f.calls.Load(); got != 3 {
		t.Fatalf("issuer called %d times, want 3 (open breaker must fail fast)", got)
	}
}

func TestBreakerIgnoresRejections(t *testing.T) {
	f := newFakeIssuer(t, 0, 400)
	cfg := testConfig()
	cfg.MaxRetries = 0
	cfg.BreakerThreshold = 3
	c, _ := newTestClient(cfg)
	for n := 0; n < 5; n++ {
		if _, err := c.Authorize(context.Background(), f.route(), req); !errors.Is(err, ErrRejected) {
			t.Fatalf("call %d: err = %v, want ErrRejected (4xx must not trip the breaker)", n, err)
		}
	}
}

func TestBreakerIsPerIssuer(t *testing.T) {
	bad := newFakeIssuer(t, 0, 503)
	good := newFakeIssuer(t, 0, 200)
	cfg := testConfig()
	cfg.MaxRetries = 0
	cfg.BreakerThreshold = 2
	c, _ := newTestClient(cfg)
	badRoute := routing.Route{IssuerID: "bad", URL: bad.srv.URL}
	goodRoute := routing.Route{IssuerID: "good", URL: good.srv.URL}
	for n := 0; n < 3; n++ {
		c.Authorize(context.Background(), badRoute, req)
	}
	if _, err := c.Authorize(context.Background(), goodRoute, req); err != nil {
		t.Fatalf("healthy issuer failed with %v; one issuer's breaker must not affect another", err)
	}
}
