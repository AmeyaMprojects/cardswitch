package breaker

import (
	"errors"
	"sync"
	"time"
)

var ErrOpen = errors.New("circuit open")

type state int

const (
	closed state = iota
	open
	halfOpen
)

// Breaker is a minimal consecutive-failure circuit breaker.
type Breaker struct {
	mu        sync.Mutex
	state     state
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
}

func New(threshold int, cooldown time.Duration) *Breaker {
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// Do runs fn unless the breaker is open.
func (b *Breaker) Do(fn func() error) error {
	b.mu.Lock()
	if b.state == open {
		if time.Since(b.openedAt) < b.cooldown {
			b.mu.Unlock()
			return ErrOpen
		}
		b.state = halfOpen
	}
	b.mu.Unlock()

	err := fn()

	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.failures++
		if b.state == halfOpen || b.failures >= b.threshold {
			b.state, b.openedAt = open, time.Now()
		}
		return err
	}
	b.state, b.failures = closed, 0
	return nil
}
