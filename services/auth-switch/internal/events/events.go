// Package events publishes authorization events for downstream services
// (settlement, ledger, analytics). Events are keyed by merchant ID so all of
// one merchant's events land on the same Kafka partition, in order.
package events

import (
	"context"
	"encoding/json"
	"time"
)

const (
	TopicApproved = "auth.approved"
	TopicReversed = "auth.reversed"
)

// Reasons a charge is reversed.
const (
	ReasonIssuerTimeout = "ISSUER_TIMEOUT"
)

type AuthEvent struct {
	ChargeID     string    `json:"charge_id"`
	MerchantID   string    `json:"merchant_id"`
	Token        string    `json:"token"`
	IssuerID     string    `json:"issuer_id"`
	AmountMinor  int64     `json:"amount_minor"`
	Currency     string    `json:"currency"`
	ResponseCode string    `json:"response_code,omitempty"`
	AuthCode     string    `json:"auth_code,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// Publisher sends one message to a topic. KafkaPublisher implements it for
// real; tests use an in-memory fake.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

type Emitter struct {
	pub Publisher
	now func() time.Time
}

func NewEmitter(pub Publisher) *Emitter {
	return &Emitter{pub: pub, now: time.Now}
}

func (e *Emitter) Approved(ctx context.Context, ev AuthEvent) error {
	return e.publish(ctx, TopicApproved, ev)
}

func (e *Emitter) Reversed(ctx context.Context, ev AuthEvent) error {
	return e.publish(ctx, TopicReversed, ev)
}

// publish stamps the event time, encodes it as JSON and sends it keyed by
// merchant ID.
func (e *Emitter) publish(ctx context.Context, topic string, ev AuthEvent) error {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = e.now().UTC()
	}
	value, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return e.pub.Publish(ctx, topic, ev.MerchantID, value)
}
