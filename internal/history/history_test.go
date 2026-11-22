package history

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/javabetatester/burpcli/internal/flow"
)

func mk(id uint64, host, body string) *flow.Flow {
	return &flow.Flow{ID: id, Request: flow.Request{Method: "GET", URL: "http://" + host + "/p", Header: http.Header{"X-K": {"Val"}}},
		Response: &flow.Response{Status: 200, Body: []byte(body)}}
}

func TestRingOverwritesOldest(t *testing.T) {
	s, _ := New(3, nil, nil)
	for i := uint64(1); i <= 5; i++ {
		s.Record(mk(i, "a.test", ""))
	}
	got := s.List(Filter{})
	if len(got) != 3 || got[0].ID != 3 || got[2].ID != 5 {
		t.Fatalf("ring = %v", got)
	}
	if _, ok := s.Get(2); ok {
		t.Fatal("evicted flow still found")
	}
	if f, ok := s.Get(4); !ok || f.ID != 4 {
		t.Fatal("Get failed")
	}
}

func TestInvalidCapacity(t *testing.T) {
	if _, err := New(0, nil, nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestScopeAndSearch(t *testing.T) {
	s, _ := New(10, nil, nil)
	s.Record(mk(1, "api.example.test", "hello needle"))
	s.Record(mk(2, "cdn.example.test", "plain"))
	s.Record(mk(3, "other.test", "needle"))
	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"all", Filter{}, 3},
		{"include glob", Filter{Scope: Scope{Include: []string{"*.example.test"}}}, 2},
		{"exclude wins", Filter{Scope: Scope{Include: []string{"*.example.test"}, Exclude: []string{"cdn.*"}}}, 1},
		{"body search", Filter{Query: "NEEDLE"}, 2},
		{"header search", Filter{Query: "val"}, 3},
		{"url search", Filter{Query: "other"}, 1},
		{"method", Filter{Method: "post"}, 0},
		{"combined", Filter{Scope: Scope{Include: []string{"other.test"}}, Query: "needle"}, 1},
	}
	for _, c := range cases {
		if got := len(s.List(c.f)); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestSQLitePersistenceReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.db")
	db, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(10, db, nil)
	for i := 0; i < 4; i++ {
		s.Record(mk(s.NextID(), fmt.Sprintf("h%d.test", i), "b"))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	s2, err := New(3, db2, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.List(Filter{})
	if len(got) != 3 || got[0].ID != 2 || string(got[2].Response.Body) != "b" {
		t.Fatalf("reloaded = %+v", got)
	}
	if id := s2.NextID(); id != 5 {
		t.Fatalf("next id = %d", id)
	}
}

func TestOnRecordObserver(t *testing.T) {
	s, _ := New(2, nil, nil)
	var got []uint64
	s.OnRecord(func(f *flow.Flow) { got = append(got, f.ID) })
	s.Record(mk(1, "a.test", ""))
	s.Record(mk(2, "a.test", ""))
	if len(got) != 2 || got[1] != 2 {
		t.Fatalf("observed = %v", got)
	}
}
