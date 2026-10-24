package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/segmentio/kafka-go"
)

type sent struct {
	topic, key string
	value      []byte
}

type fakePublisher struct {
	msgs []sent
	err  error
}

func (f *fakePublisher) Publish(_ context.Context, topic, key string, value []byte) error {
	f.msgs = append(f.msgs, sent{topic, key, value})
	return f.err
}

var fixed = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func newTestEmitter() (*Emitter, *fakePublisher) {
	pub := &fakePublisher{}
	e := NewEmitter(pub)
	e.now = func() time.Time { return fixed }
	return e, pub
}

var ev = AuthEvent{ChargeID: "c1", MerchantID: "m_42", Token: "tok_a", IssuerID: "issuer-a", AmountMinor: 1999, Currency: "USD"}

func TestEmitterTopicsAndKey(t *testing.T) {
	e, pub := newTestEmitter()
	if err := e.Approved(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if err := e.Reversed(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(pub.msgs) != 2 {
		t.Fatalf("published %d messages, want 2", len(pub.msgs))
	}
	if pub.msgs[0].topic != TopicApproved || pub.msgs[1].topic != TopicReversed {
		t.Fatalf("topics = %q, %q", pub.msgs[0].topic, pub.msgs[1].topic)
	}
	for _, m := range pub.msgs {
		if m.key != "m_42" {
			t.Fatalf("key = %q, want merchant ID m_42 so a merchant's events stay in order", m.key)
		}
	}
}

func TestEmitterPayload(t *testing.T) {
	e, pub := newTestEmitter()
	e.Reversed(context.Background(), ev)

	var got map[string]any
	if err := json.Unmarshal(pub.msgs[0].value, &got); err != nil {
		t.Fatalf("value is not JSON: %v", err)
	}
	if got["charge_id"] != "c1" || got["amount_minor"] != float64(1999) {
		t.Fatalf("payload = %v, want snake_case fields from AuthEvent's tags", got)
	}
	if got["occurred_at"] != "2026-10-24T12:00:00Z" {
		t.Fatalf("occurred_at = %v, want the emitter's clock in UTC when unset", got["occurred_at"])
	}
}

func TestEmitterKeepsGivenTime(t *testing.T) {
	e, pub := newTestEmitter()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	withTime := ev
	withTime.OccurredAt = when
	e.Approved(context.Background(), withTime)
	var got AuthEvent
	json.Unmarshal(pub.msgs[0].value, &got)
	if !got.OccurredAt.Equal(when) {
		t.Fatalf("occurred_at = %v, want caller's %v kept", got.OccurredAt, when)
	}
}

func TestEmitterReturnsPublishError(t *testing.T) {
	e, pub := newTestEmitter()
	pub.err = errors.New("broker down")
	if err := e.Approved(context.Background(), ev); !errors.Is(err, pub.err) {
		t.Fatalf("err = %v, want the publisher's error", err)
	}
}

type fakeWriter struct {
	msgs   []kafka.Message
	closed bool
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	f.msgs = append(f.msgs, msgs...)
	return nil
}

func (f *fakeWriter) Close() error { f.closed = true; return nil }

func TestKafkaPublisherMessage(t *testing.T) {
	w := &fakeWriter{}
	p := &KafkaPublisher{w: w}
	if err := p.Publish(context.Background(), "auth.reversed", "m_42", []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if len(w.msgs) != 1 {
		t.Fatalf("wrote %d messages, want 1", len(w.msgs))
	}
	m := w.msgs[0]
	if m.Topic != "auth.reversed" || string(m.Key) != "m_42" || string(m.Value) != `{"x":1}` {
		t.Fatalf("message = topic %q key %q value %q", m.Topic, m.Key, m.Value)
	}
	p.Close()
	if !w.closed {
		t.Fatal("Close must close the writer so buffered messages are flushed")
	}
}

func TestKafkaPublisherConfig(t *testing.T) {
	p := NewKafkaPublisher("kafka:9092")
	w, ok := p.w.(*kafka.Writer)
	if !ok {
		t.Fatalf("writer is %T, want *kafka.Writer", p.w)
	}
	if w.Addr == nil || w.Addr.String() != "kafka:9092" {
		t.Fatalf("Addr = %v, want kafka:9092", w.Addr)
	}
	if _, ok := w.Balancer.(*kafka.Hash); !ok {
		t.Fatalf("Balancer = %T, want *kafka.Hash so equal keys go to one partition", w.Balancer)
	}
	if w.RequiredAcks != kafka.RequireAll {
		t.Fatalf("RequiredAcks = %v, want RequireAll so a broker crash can't lose an acked event", w.RequiredAcks)
	}
	if w.Topic != "" {
		t.Fatalf("Topic = %q; leave it empty so each message picks its own topic", w.Topic)
	}
}
