package charges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

type idemKey struct{ merchant, key string }

type fakeStore struct {
	mu      sync.Mutex
	recs    map[idemKey]Record
	charges map[string]Charge
	err     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{recs: map[idemKey]Record{}, charges: map[string]Charge{}}
}

func (f *fakeStore) BeginIdempotent(_ context.Context, m, k string, hash []byte) (Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Record{}, false, f.err
	}
	if r, ok := f.recs[idemKey{m, k}]; ok {
		return r, false, nil
	}
	f.recs[idemKey{m, k}] = Record{RequestHash: hash}
	return Record{}, true, nil
}

func (f *fakeStore) GetIdempotent(_ context.Context, m, k string) (Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.recs[idemKey{m, k}]
	return r, ok, f.err
}

func (f *fakeStore) CompleteIdempotent(_ context.Context, m, k string, status int, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.recs[idemKey{m, k}]
	r.Status, r.Body = status, body
	f.recs[idemKey{m, k}] = r
	return nil
}

func (f *fakeStore) AbortIdempotent(_ context.Context, m, k string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.recs, idemKey{m, k})
	return nil
}

func (f *fakeStore) CreateCharge(_ context.Context, c Charge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.charges[c.ID] = c
	return nil
}

func (f *fakeStore) UpdateCharge(_ context.Context, c Charge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.charges[c.ID] = c
	return nil
}

type fakeLocker struct {
	mu    sync.Mutex
	held  map[string]bool
	err   error
	block bool // pretend someone else holds every lock
}

func (f *fakeLocker) Acquire(_ context.Context, key string, _ time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	if f.block || f.held[key] {
		return "", nil
	}
	if f.held == nil {
		f.held = map[string]bool{}
	}
	f.held[key] = true
	return "tok", nil
}

func (f *fakeLocker) Release(_ context.Context, key, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.held, key)
	return nil
}

type fakeVault struct{ err error }

func (f fakeVault) Tokenize(_ context.Context, pan string) (TokenInfo, error) {
	if f.err != nil {
		return TokenInfo{}, f.err
	}
	return TokenInfo{Token: "tok_" + pan[len(pan)-4:], BIN: pan[:6], Last4: pan[len(pan)-4:]}, nil
}

func (f fakeVault) Lookup(_ context.Context, token string) (TokenInfo, error) {
	return TokenInfo{Token: token, BIN: "411111", Last4: "1111"}, f.err
}

type fakeSwitch struct {
	mu    sync.Mutex
	calls int
	code  string
	err   error
	gate  chan struct{} // if set, Authorize waits on it
}

func (f *fakeSwitch) Authorize(_ context.Context, req SwitchRequest) (SwitchResponse, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return SwitchResponse{}, f.err
	}
	return SwitchResponse{ChargeID: req.ChargeID, ResponseCode: f.code, AuthCode: "123456", IssuerID: "issuer-a"}, nil
}

type fakeLedger struct {
	mu    sync.Mutex
	holds []Charge
}

func (f *fakeLedger) PostHold(_ context.Context, c Charge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holds = append(f.holds, c)
	return nil
}

type rig struct {
	svc    *Service
	store  *fakeStore
	locker *fakeLocker
	sw     *fakeSwitch
	ledger *fakeLedger
}

func newRig(code string) *rig {
	r := &rig{store: newFakeStore(), locker: &fakeLocker{}, sw: &fakeSwitch{code: code}, ledger: &fakeLedger{}}
	n := 0
	r.svc = NewService(r.locker, r.store, fakeVault{}, r.sw, r.ledger)
	r.svc.NewID = func() string { n++; return fmt.Sprintf("ch_%d", n) }
	return r
}

var good = Request{MerchantID: "m_1", AmountMinor: 1999, Currency: "USD", PAN: "4111111111111111"}

func decode(t *testing.T, res Result) Response {
	t.Helper()
	var r Response
	if err := json.Unmarshal(res.Body, &r); err != nil {
		t.Fatalf("body %q is not JSON: %v", res.Body, err)
	}
	return r
}

func TestApprovedCharge(t *testing.T) {
	r := newRig("00")
	res := r.svc.Charge(context.Background(), "k1", good)
	if res.HTTPStatus != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", res.HTTPStatus, res.Body)
	}
	body := decode(t, res)
	if body.Status != StatusApproved || body.ChargeID != "ch_1" || body.Last4 != "1111" {
		t.Fatalf("body = %+v", body)
	}
	if body.Token == "" || body.Token == good.PAN {
		t.Fatal("response must carry the token, never the PAN")
	}
	if len(r.ledger.holds) != 1 || r.ledger.holds[0].IssuerID != "issuer-a" {
		t.Fatalf("ledger holds = %+v, want one hold for issuer-a", r.ledger.holds)
	}
	if c := r.store.charges["ch_1"]; c.Status != StatusApproved || c.AuthCode != "123456" {
		t.Fatalf("stored charge = %+v", c)
	}
}

func TestDeclinedChargeSkipsLedger(t *testing.T) {
	r := newRig("51")
	res := r.svc.Charge(context.Background(), "k1", good)
	if res.HTTPStatus != http.StatusPaymentRequired || decode(t, res).ResponseCode != "51" {
		t.Fatalf("status = %d body = %s, want 402 with code 51", res.HTTPStatus, res.Body)
	}
	if len(r.ledger.holds) != 0 {
		t.Fatal("a decline must not post a ledger hold")
	}
}

func TestRetryReplaysStoredResponse(t *testing.T) {
	r := newRig("00")
	first := r.svc.Charge(context.Background(), "k1", good)
	second := r.svc.Charge(context.Background(), "k1", good)
	if string(first.Body) != string(second.Body) || second.HTTPStatus != first.HTTPStatus {
		t.Fatalf("replay differs:\n first %d %s\nsecond %d %s", first.HTTPStatus, first.Body, second.HTTPStatus, second.Body)
	}
	if !second.Replayed {
		t.Fatal("second response should be marked as replayed")
	}
	if r.sw.calls != 1 || len(r.ledger.holds) != 1 {
		t.Fatalf("switch calls = %d, holds = %d, want 1 and 1", r.sw.calls, len(r.ledger.holds))
	}
}

func TestSameKeyDifferentBody(t *testing.T) {
	r := newRig("00")
	r.svc.Charge(context.Background(), "k1", good)
	other := good
	other.AmountMinor = 5000
	if res := r.svc.Charge(context.Background(), "k1", other); res.HTTPStatus != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.HTTPStatus)
	}
}

func TestKeysAreScopedPerMerchant(t *testing.T) {
	r := newRig("00")
	r.svc.Charge(context.Background(), "k1", good)
	other := good
	other.MerchantID = "m_2"
	if res := r.svc.Charge(context.Background(), "k1", other); res.Replayed || res.HTTPStatus != http.StatusOK {
		t.Fatalf("another merchant's k1 must be a new charge, got %d replayed=%v", res.HTTPStatus, res.Replayed)
	}
}

func TestConcurrentDuplicatesChargeOnce(t *testing.T) {
	r := newRig("00")
	r.sw.gate = make(chan struct{})
	var wg sync.WaitGroup
	results := make([]Result, 20)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.svc.Charge(context.Background(), "k1", good)
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(r.sw.gate)
	wg.Wait()

	ok, conflict := 0, 0
	for _, res := range results {
		switch res.HTTPStatus {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("unexpected status %d: %s", res.HTTPStatus, res.Body)
		}
	}
	if r.sw.calls != 1 {
		t.Fatalf("switch called %d times for one key, want 1", r.sw.calls)
	}
	if ok < 1 || ok+conflict != 20 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestInFlightWithoutRecordIsConflict(t *testing.T) {
	r := newRig("00")
	r.locker.block = true
	if res := r.svc.Charge(context.Background(), "k1", good); res.HTTPStatus != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.HTTPStatus)
	}
	if len(r.store.recs) != 0 {
		t.Fatal("a request that lost the lock must not create an idempotency record")
	}
}

func TestRedisDownFallsBackToPostgres(t *testing.T) {
	r := newRig("00")
	r.locker.err = errors.New("redis down")
	first := r.svc.Charge(context.Background(), "k1", good)
	second := r.svc.Charge(context.Background(), "k1", good)
	if first.HTTPStatus != http.StatusOK || !second.Replayed || r.sw.calls != 1 {
		t.Fatalf("first=%d replayed=%v calls=%d; Postgres alone must still dedupe", first.HTTPStatus, second.Replayed, r.sw.calls)
	}
}

func TestVaultFailureLetsClientRetry(t *testing.T) {
	r := newRig("00")
	r.svc.Vault = fakeVault{err: errors.New("vault down")}
	if res := r.svc.Charge(context.Background(), "k1", good); res.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", res.HTTPStatus)
	}
	r.svc.Vault = fakeVault{}
	if res := r.svc.Charge(context.Background(), "k1", good); res.HTTPStatus != http.StatusOK || res.Replayed {
		t.Fatalf("retry after vault recovery: status=%d replayed=%v, want a fresh 200", res.HTTPStatus, res.Replayed)
	}
}

func TestInvalidCardIs400(t *testing.T) {
	r := newRig("00")
	r.svc.Vault = fakeVault{err: ErrInvalidCard}
	if res := r.svc.Charge(context.Background(), "k1", good); res.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.HTTPStatus)
	}
}

func TestSwitchFailureIsNotRetried(t *testing.T) {
	r := newRig("00")
	r.sw.err = errors.New("switch timeout")
	first := r.svc.Charge(context.Background(), "k1", good)
	if first.HTTPStatus != http.StatusBadGateway || decode(t, first).Status != StatusPending {
		t.Fatalf("status = %d body = %s, want 502 PENDING", first.HTTPStatus, first.Body)
	}
	r.sw.err = nil
	second := r.svc.Charge(context.Background(), "k1", good)
	if !second.Replayed || r.sw.calls != 1 {
		t.Fatalf("retry after an ambiguous switch failure must replay, not re-send (calls=%d)", r.sw.calls)
	}
}

func TestValidation(t *testing.T) {
	r := newRig("00")
	cases := map[string]Request{
		"no merchant":   {AmountMinor: 1, Currency: "USD", PAN: "4111111111111111"},
		"zero amount":   {MerchantID: "m", Currency: "USD", PAN: "4111111111111111"},
		"bad currency":  {MerchantID: "m", AmountMinor: 1, Currency: "usd", PAN: "4111111111111111"},
		"no card":       {MerchantID: "m", AmountMinor: 1, Currency: "USD"},
		"pan and token": {MerchantID: "m", AmountMinor: 1, Currency: "USD", PAN: "4111111111111111", Token: "tok"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if res := r.svc.Charge(context.Background(), "k", req); res.HTTPStatus != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.HTTPStatus)
			}
		})
	}
	if res := r.svc.Charge(context.Background(), "", good); res.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("missing key: status = %d, want 400", res.HTTPStatus)
	}
}
