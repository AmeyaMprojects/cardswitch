package events

import (
	"context"

	"github.com/segmentio/kafka-go"
)

// messageWriter is the part of *kafka.Writer we use, so tests can fake it.
type messageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// KafkaPublisher publishes to Kafka. One writer serves every topic.
type KafkaPublisher struct {
	w messageWriter
}

func NewKafkaPublisher(brokers ...string) *KafkaPublisher {
	return &KafkaPublisher{w: &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
	}}
}

func (p *KafkaPublisher) Publish(ctx context.Context, topic, key string, value []byte) error {
	return p.w.WriteMessages(ctx, kafka.Message{Topic: topic, Key: []byte(key), Value: value})
}

func (p *KafkaPublisher) Close() error {
	return p.w.Close()
}
