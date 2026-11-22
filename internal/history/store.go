// Package history keeps recorded flows in a ring buffer with optional durable persistence.
package history

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/javabetatester/burpcli/internal/flow"
)

// Persister durably stores flows; implementations must be safe for concurrent use.
type Persister interface {
	Save(f *flow.Flow) error
	Load(limit int) ([]*flow.Flow, error)
}

// Filter narrows a listing. Zero value matches everything.
type Filter struct {
	Scope  Scope
	Query  string
	Method string
}

// Store is a bounded in-memory history, newest overwriting oldest.
type Store struct {
	persist Persister
	log     *slog.Logger
	nextID  atomic.Uint64
	version atomic.Uint64

	mu    sync.RWMutex
	ring  []*flow.Flow
	start int
	size  int
}

// New builds a store holding at most capacity flows. persist and log may be nil; persisted flows are reloaded.
func New(capacity int, persist Persister, log *slog.Logger) (*Store, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("history: capacity must be positive, got %d", capacity)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Store{persist: persist, log: log, ring: make([]*flow.Flow, capacity)}
	if persist != nil {
		old, err := persist.Load(capacity)
		if err != nil {
			return nil, fmt.Errorf("history: load persisted flows: %w", err)
		}
		for _, f := range old {
			s.push(f)
			if f.ID > s.nextID.Load() {
				s.nextID.Store(f.ID)
			}
		}
	}
	return s, nil
}

// NextID reserves a flow ID.
func (s *Store) NextID() uint64 { return s.nextID.Add(1) }

// Version changes whenever the store content changes.
func (s *Store) Version() uint64 { return s.version.Load() }

// Record stores a finished flow and persists it when a Persister is configured.
func (s *Store) Record(f *flow.Flow) {
	s.push(f)
	if s.persist != nil {
		if err := s.persist.Save(f); err != nil {
			s.log.Error("persist flow", "id", f.ID, "err", err)
		}
	}
}

func (s *Store) push(f *flow.Flow) {
	s.mu.Lock()
	if s.size < len(s.ring) {
		s.ring[(s.start+s.size)%len(s.ring)] = f
		s.size++
	} else {
		s.ring[s.start] = f
		s.start = (s.start + 1) % len(s.ring)
	}
	s.mu.Unlock()
	s.version.Add(1)
}

// Get returns the flow with the given ID, if still in the ring.
func (s *Store) Get(id uint64) (*flow.Flow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := 0; i < s.size; i++ {
		if f := s.ring[(s.start+i)%len(s.ring)]; f.ID == id {
			return f, true
		}
	}
	return nil, false
}

// List returns matching flows, oldest first.
func (s *Store) List(flt Filter) []*flow.Flow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*flow.Flow, 0, s.size)
	for i := 0; i < s.size; i++ {
		if f := s.ring[(s.start+i)%len(s.ring)]; flt.matches(f) {
			out = append(out, f)
		}
	}
	return out
}

func (flt Filter) matches(f *flow.Flow) bool {
	if !flt.Scope.Match(f.Request.Hostname()) {
		return false
	}
	if flt.Method != "" && !strings.EqualFold(flt.Method, f.Request.Method) {
		return false
	}
	return flt.Query == "" || search(f, strings.ToLower(flt.Query))
}

func search(f *flow.Flow, q string) bool {
	if strings.Contains(strings.ToLower(f.Request.URL), q) ||
		headerHas(f.Request.Header, q) || bytes.Contains(bytes.ToLower(f.Request.Body), []byte(q)) {
		return true
	}
	if r := f.Response; r != nil {
		return headerHas(r.Header, q) || bytes.Contains(bytes.ToLower(r.Body), []byte(q))
	}
	return false
}

func headerHas(h http.Header, q string) bool {
	for k, vs := range h {
		if strings.Contains(strings.ToLower(k), q) {
			return true
		}
		for _, v := range vs {
			if strings.Contains(strings.ToLower(v), q) {
				return true
			}
		}
	}
	return false
}
