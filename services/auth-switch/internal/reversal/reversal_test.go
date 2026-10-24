package reversal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/events"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

type fakeIssuer struct {
	mu       sync.Mutex
	calls    []string
	deadline time.Time
	release  chan struct{} // if set, Reverse blocks until it's closed
}

func (f *fakeIssuer) Reverse(ctx context.Context, _ routing.Route, chargeID string) error {
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, chargeID)
	f.deadline, _ = ctx.Deadline()
	return ctx.Err()
}

type fakeEvents struct {
	mu  sync.Mutex
	got []events.AuthEvent
	err error
}

func (f *fakeEvents) Reversed(_ context.Context, ev events.AuthEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, ev)
	return f.err
}

var (
	route = routing.Route{IssuerID: "issuer-a", URL: "http://issuer"}
	ev    = events.AuthEvent{ChargeID: "c1", MerchantID: "m_42", Reason: events.ReasonIssuerTimeout}
)

func TestReversePublishesAndCallsIssuer(t *testing.T) {
	iss, evs := &fakeIssuer{}, &fakeEvents{}
	r := New(iss, evs, time.Second, time.Minute)
	if err := r.Reverse(ev, route); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(evs.got) != 1 || evs.got[0].ChargeID != "c1" {
		t.Fatalf("events = %+v, want one reversal for c1, published before Reverse returns", evs.got)
	}
	r.Wait()
	if len(iss.calls) != 1 || iss.calls[0] != "c1" {
		t.Fatalf("issuer calls = %v, want [c1]", iss.calls)
	}
}

func TestReverseDoesNotWaitForIssuer(t *testing.T) {
	iss := &fakeIssuer{release: make(chan struct{})}
	r := New(iss, &fakeEvents{}, time.Second, time.Minute)

	done := make(chan struct{})
	go func() { r.Reverse(ev, route); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Reverse blocked on the issuer; run the issuer call in a goroutine")
	}

	waited := make(chan struct{})
	go func() { r.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned while a reversal was still running; use a sync.WaitGroup")
	case <-time.After(50 * time.Millisecond):
	}
	close(iss.release)
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("Wait never returned after the reversal finished")
	}
}

func TestReverseUsesItsOwnDeadline(t *testing.T) {
	iss := &fakeIssuer{}
	r := New(iss, &fakeEvents{}, time.Second, time.Minute)
	start := time.Now()
	r.Reverse(ev, route)
	r.Wait()
	if iss.deadline.IsZero() {
		t.Fatal("issuer ctx has no deadline; bound it with reverseTimeout")
	}
	if left := iss.deadline.Sub(start); left < 50*time.Second || left > 70*time.Second {
		t.Fatalf("issuer deadline is %v away, want about reverseTimeout (1m)", left)
	}
}

func TestReverseRunsEvenIfPublishFails(t *testing.T) {
	iss := &fakeIssuer{}
	evs := &fakeEvents{err: errors.New("kafka down")}
	r := New(iss, evs, time.Second, time.Minute)
	if err := r.Reverse(ev, route); !errors.Is(err, evs.err) {
		t.Fatalf("err = %v, want the publish error returned", err)
	}
	r.Wait()
	if len(iss.calls) != 1 {
		t.Fatal("issuer reversal must still run when Kafka fails: the cardholder's money comes first")
	}
}
