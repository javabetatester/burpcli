package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/javabetatester/burpcli/internal/flow"
)

// ErrNotPending reports a decision for a request that is no longer held.
var ErrNotPending = errors.New("pipeline: request not pending")

// ErrHoldTimeout reports a held request nobody resolved in time.
var ErrHoldTimeout = fmt.Errorf("%w: hold timed out", ErrDropped)

// Action is the operator verdict on a held request.
type Action int

// Operator verdicts.
const (
	Forward Action = iota
	Drop
)

// Decision resolves a held request; Edited, when set, replaces it before forwarding.
type Decision struct {
	Action Action
	Edited *flow.Request
}

// Held is a read-only view of a request waiting for a decision.
type Held struct {
	ID      uint64
	Request flow.Request
	Since   time.Time
}

type pending struct {
	held Held
	ch   chan Decision
}

// Queue is an Interceptor that holds matching requests until resolved or timed out.
type Queue struct {
	Passthrough
	timeout time.Duration
	match   func(host string) bool
	enabled atomic.Bool
	mu      sync.Mutex
	held    map[uint64]*pending
}

// NewQueue builds a queue. A held request is dropped after timeout; match limits which hosts are held (nil holds all).
func NewQueue(timeout time.Duration, match func(host string) bool) *Queue {
	if match == nil {
		match = func(string) bool { return true }
	}
	return &Queue{timeout: timeout, match: match, held: map[uint64]*pending{}}
}

// Enabled reports whether new requests are held.
func (q *Queue) Enabled() bool { return q.enabled.Load() }

// SetEnabled toggles holding. Disabling forwards everything currently held.
func (q *Queue) SetEnabled(on bool) {
	q.enabled.Store(on)
	if on {
		return
	}
	for _, h := range q.Pending() {
		_ = q.Resolve(h.ID, Decision{Action: Forward})
	}
}

// Request holds the request until Resolve, timeout or context cancellation.
func (q *Queue) Request(ctx context.Context, id uint64, req *flow.Request) error {
	if !q.enabled.Load() || !q.match(req.Hostname()) {
		return nil
	}
	p := &pending{held: Held{ID: id, Request: req.Clone(), Since: time.Now()}, ch: make(chan Decision, 1)}
	q.mu.Lock()
	q.held[id] = p
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.held, id)
		q.mu.Unlock()
	}()
	timer := time.NewTimer(q.timeout)
	defer timer.Stop()
	select {
	case d := <-p.ch:
		if d.Action == Drop {
			return ErrDropped
		}
		if d.Edited != nil {
			*req = d.Edited.Clone()
		}
		return nil
	case <-timer.C:
		return ErrHoldTimeout
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrDropped, ctx.Err())
	}
}

// Pending lists held requests, oldest first.
func (q *Queue) Pending() []Held {
	q.mu.Lock()
	out := make([]Held, 0, len(q.held))
	for _, p := range q.held {
		out = append(out, p.held)
	}
	q.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Resolve delivers a decision to a held request.
func (q *Queue) Resolve(id uint64, d Decision) error {
	q.mu.Lock()
	p, ok := q.held[id]
	if ok {
		delete(q.held, id)
	}
	q.mu.Unlock()
	if !ok {
		return ErrNotPending
	}
	p.ch <- d
	return nil
}
