package issuer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestNew(t *testing.T) {
	iss := New(10_000)
	if iss == nil {
		t.Fatal("New returned nil")
	}
	if iss.accounts == nil || iss.auths == nil {
		t.Fatal("maps must be initialized with make(); writing to a nil map panics")
	}
	if iss.defaultLimit != 10_000 {
		t.Fatalf("defaultLimit = %d, want 10000", iss.defaultLimit)
	}
}

func TestAccountCreatedWithDefaultLimit(t *testing.T) {
	iss := New(5_000)
	if got := iss.Available("tok_new"); got != 5_000 {
		t.Fatalf("Available(new token) = %d, want 5000", got)
	}
	iss.Block("tok_new")
	if !iss.accounts["tok_new"].Blocked {
		t.Fatal("Block should mark the account as blocked")
	}
}

// Table-driven: each row is one decision scenario.
func TestAuthorizeDecisions(t *testing.T) {
	tests := []struct {
		name      string
		amount    int64
		blocked   bool
		wantCode  string
		wantAvail int64
	}{
		{"approve within limit", 1_999, false, CodeApproved, 8_001},
		{"approve exact limit", 10_000, false, CodeApproved, 0},
		{"insufficient funds", 10_001, false, CodeInsufficientFunds, 10_000},
		{"zero amount", 0, false, CodeInvalidAmount, 10_000},
		{"negative amount", -5, false, CodeInvalidAmount, 10_000},
		{"blocked card", 100, true, CodeDoNotHonor, 10_000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			iss := New(10_000)
			if tc.blocked {
				iss.Block("tok_a")
			}
			resp := iss.Authorize(AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: tc.amount, Currency: "USD"})
			if resp.ResponseCode != tc.wantCode {
				t.Fatalf("code = %q, want %q", resp.ResponseCode, tc.wantCode)
			}
			if resp.ChargeID != "c1" {
				t.Fatalf("ChargeID = %q, want echo of request", resp.ChargeID)
			}
			if got := iss.Available("tok_a"); got != tc.wantAvail {
				t.Fatalf("available = %d, want %d", got, tc.wantAvail)
			}
			if tc.wantCode == CodeApproved && len(resp.AuthCode) != 6 {
				t.Fatalf("approved auth code %q should be 6 digits", resp.AuthCode)
			}
			if tc.wantCode != CodeApproved && resp.AuthCode != "" {
				t.Fatalf("declines must not carry an auth code, got %q", resp.AuthCode)
			}
		})
	}
}

// Same charge_id twice must return the same answer and debit once.
func TestAuthorizeReplayIsIdempotent(t *testing.T) {
	iss := New(10_000)
	req := AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 3_000, Currency: "USD"}
	first := iss.Authorize(req)
	second := iss.Authorize(req)
	if first != second {
		t.Fatalf("replay returned %+v, want identical %+v", second, first)
	}
	if got := iss.Available("tok_a"); got != 7_000 {
		t.Fatalf("available = %d, want 7000 (money taken once, not twice)", got)
	}
}

// Run with -race: catches a missing lock around check-then-subtract.
func TestAuthorizeConcurrent(t *testing.T) {
	iss := New(1_000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	approved := 0
	for n := 0; n < 50; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			resp := iss.Authorize(AuthRequest{ChargeID: fmt.Sprintf("c%d", n), Token: "tok_a", AmountMinor: 100})
			if resp.ResponseCode == CodeApproved {
				mu.Lock()
				approved++
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	if approved != 10 {
		t.Fatalf("approved %d auths of 100 against a 1000 limit, want exactly 10", approved)
	}
	if got := iss.Available("tok_a"); got != 0 {
		t.Fatalf("available = %d, want 0", got)
	}
}

func TestHandler(t *testing.T) {
	h := New(10_000).Handler()

	t.Run("approves valid JSON", func(t *testing.T) {
		body := `{"charge_id":"c1","token":"tok_a","amount_minor":1999,"currency":"USD"}`
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/authorize", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", ct)
		}
		var resp AuthResponse
		if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
			t.Fatalf("response is not JSON: %v", err)
		}
		if resp.ResponseCode != CodeApproved || resp.ChargeID != "c1" {
			t.Fatalf("got %+v, want approved c1", resp)
		}
	})

	for name, body := range map[string]string{
		"malformed JSON":    `{"charge_id":`,
		"missing charge_id": `{"token":"tok_a","amount_minor":100}`,
		"missing token":     `{"charge_id":"c2","amount_minor":100}`,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/authorize", strings.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}

	t.Run("rejects GET", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/authorize", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})
}

func TestReverse(t *testing.T) {
	t.Run("releases an approved hold", func(t *testing.T) {
		iss := New(10_000)
		iss.Authorize(AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 3_000})
		if got := iss.Reverse("c1"); got.Result != ReverseReleased || got.ChargeID != "c1" {
			t.Fatalf("Reverse = %+v, want released c1", got)
		}
		if got := iss.Available("tok_a"); got != 10_000 {
			t.Fatalf("available = %d, want 10000 after release", got)
		}
	})
	t.Run("second reversal is a no-op", func(t *testing.T) {
		iss := New(10_000)
		iss.Authorize(AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 3_000})
		iss.Reverse("c1")
		if got := iss.Reverse("c1"); got.Result != ReverseAlreadyReversed {
			t.Fatalf("second Reverse = %q, want %q", got.Result, ReverseAlreadyReversed)
		}
		if got := iss.Available("tok_a"); got != 10_000 {
			t.Fatalf("available = %d, want 10000 (money given back once, not twice)", got)
		}
	})
	t.Run("declined charge has no hold", func(t *testing.T) {
		iss := New(1_000)
		iss.Authorize(AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 5_000})
		if got := iss.Reverse("c1"); got.Result != ReverseNoHold {
			t.Fatalf("Reverse = %q, want %q", got.Result, ReverseNoHold)
		}
		if got := iss.Available("tok_a"); got != 1_000 {
			t.Fatalf("available = %d, want 1000 (a decline took nothing, so nothing to give back)", got)
		}
	})
	t.Run("reversal before the authorization blocks it", func(t *testing.T) {
		iss := New(10_000)
		if got := iss.Reverse("c1"); got.Result != ReverseNoHold {
			t.Fatalf("Reverse = %q, want %q", got.Result, ReverseNoHold)
		}
		late := iss.Authorize(AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 3_000})
		if late.ResponseCode != CodeDoNotHonor {
			t.Fatalf("late authorization = %q, want %q (already reversed)", late.ResponseCode, CodeDoNotHonor)
		}
		if got := iss.Available("tok_a"); got != 10_000 {
			t.Fatalf("available = %d, want 10000 (late auth must not take money)", got)
		}
	})
}

func TestReverseHandler(t *testing.T) {
	iss := New(10_000)
	iss.Authorize(AuthRequest{ChargeID: "c1", Token: "tok_a", AmountMinor: 3_000})
	h := iss.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/reverse", strings.NewReader(`{"charge_id":"c1"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp ReverseResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.Result != ReverseReleased {
		t.Fatalf("resp = %+v err = %v, want released", resp, err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/reverse", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing charge_id: status = %d, want 400", rec.Code)
	}
}
