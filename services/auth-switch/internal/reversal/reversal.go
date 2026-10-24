// Package reversal cancels authorizations whose outcome is unknown: the
// issuer may have approved them, but the switch never got the answer.
package reversal

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/events"
	"github.com/AmeyaMprojects/cardswitch/services/auth-switch/internal/routing"
)

// IssuerReverser is the part of issuerclient.Client this package needs.
type IssuerReverser interface {
	Reverse(ctx context.Context, route routing.Route, chargeID string) error
}

// EventPublisher is the part of events.Emitter this package needs.
type EventPublisher interface {
	Reversed(ctx context.Context, ev events.AuthEvent) error
}

type Reverser struct {
	issuer         IssuerReverser
	events         EventPublisher
	publishTimeout time.Duration // how long to wait for Kafka
	reverseTimeout time.Duration // how long to keep retrying the issuer
	wg             sync.WaitGroup
}

func New(issuer IssuerReverser, ev EventPublisher, publishTimeout, reverseTimeout time.Duration) *Reverser {
	return &Reverser{issuer: issuer, events: ev, publishTimeout: publishTimeout, reverseTimeout: reverseTimeout}
}

// Reverse tells the issuer to cancel the charge in the background, then
// publishes the reversal event. The issuer reversal runs even if publishing
// fails: releasing the cardholder's money must not depend on Kafka.
// It returns the publish error, without waiting for the issuer.
func (r *Reverser) Reverse(ev events.AuthEvent, route routing.Route) error {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), r.reverseTimeout)
		defer cancel()
		if err := r.issuer.Reverse(ctx, route, ev.ChargeID); err != nil {
			log.Printf("reversal of %s at %s failed: %v", ev.ChargeID, route.IssuerID, err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), r.publishTimeout)
	defer cancel()
	return r.events.Reversed(ctx, ev)
}

// Wait blocks until every background reversal has finished.
func (r *Reverser) Wait() {
	r.wg.Wait()
}
