package pipeline

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/javabetatester/burpcli/internal/flow"
)

type tagger struct {
	Passthrough
	tag string
	err error
}

func (t tagger) Request(_ context.Context, _ uint64, r *flow.Request) error {
	r.Header.Add("X-Order", t.tag)
	return t.err
}

func newReq() *flow.Request {
	return &flow.Request{Method: "GET", URL: "http://a.test/", Header: http.Header{}}
}

func TestChainOrderAndShortCircuit(t *testing.T) {
	boom := errors.New("boom")
	r := newReq()
	err := Chain{tagger{tag: "1"}, tagger{tag: "2", err: boom}, tagger{tag: "3"}}.Request(context.Background(), 1, r)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := r.Header["X-Order"]; len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Fatalf("order = %v", got)
	}
}

func hold(q *Queue, r *flow.Request, ctx context.Context) chan error {
	done := make(chan error, 1)
	go func() { done <- q.Request(ctx, 7, r) }()
	for i := 0; i < 200 && len(q.Pending()) == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	return done
}

func TestQueueDisabledPassesThrough(t *testing.T) {
	q := NewQueue(time.Second, nil)
	if err := q.Request(context.Background(), 1, newReq()); err != nil {
		t.Fatal(err)
	}
}

func TestQueueForwardEdited(t *testing.T) {
	q := NewQueue(time.Second, nil)
	q.SetEnabled(true)
	r := newReq()
	done := hold(q, r, context.Background())
	edited := newReq()
	edited.URL = "http://a.test/edited"
	if err := q.Resolve(7, Decision{Action: Forward, Edited: edited}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil || r.URL != edited.URL {
		t.Fatalf("err=%v url=%s", err, r.URL)
	}
	if err := q.Resolve(7, Decision{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("want ErrNotPending, got %v", err)
	}
}

func TestQueueDrop(t *testing.T) {
	q := NewQueue(time.Second, nil)
	q.SetEnabled(true)
	done := hold(q, newReq(), context.Background())
	_ = q.Resolve(7, Decision{Action: Drop})
	if err := <-done; !errors.Is(err, ErrDropped) {
		t.Fatalf("err = %v", err)
	}
}

func TestQueueTimeoutNeverBlocksForever(t *testing.T) {
	q := NewQueue(30*time.Millisecond, nil)
	q.SetEnabled(true)
	start := time.Now()
	if err := q.Request(context.Background(), 1, newReq()); !errors.Is(err, ErrHoldTimeout) || !errors.Is(err, ErrDropped) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second || len(q.Pending()) != 0 {
		t.Fatal("hold leaked")
	}
}

func TestQueueContextCancel(t *testing.T) {
	q := NewQueue(time.Minute, nil)
	q.SetEnabled(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := hold(q, newReq(), ctx)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestQueueDisableReleasesAndScope(t *testing.T) {
	q := NewQueue(time.Minute, func(h string) bool { return h == "a.test" })
	q.SetEnabled(true)
	other := newReq()
	other.URL = "http://b.test/"
	if err := q.Request(context.Background(), 2, other); err != nil {
		t.Fatal("out-of-scope request held")
	}
	done := hold(q, newReq(), context.Background())
	q.SetEnabled(false)
	if err := <-done; err != nil {
		t.Fatalf("err = %v", err)
	}
}
