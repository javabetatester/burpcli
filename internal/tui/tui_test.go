package tui

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/javabetatester/burpcli/internal/flow"
	"github.com/javabetatester/burpcli/internal/history"
	"github.com/javabetatester/burpcli/internal/pipeline"
)

func newTestModel(t *testing.T) (*model, *history.Store, *pipeline.Queue) {
	t.Helper()
	h, _ := history.New(10, nil, nil)
	q := pipeline.NewQueue(time.Second, nil)
	m := newModel(context.Background(), Deps{History: h, Queue: q})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, h, q
}

func key(m *model, s string) {
	switch s {
	case "tab":
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
	default:
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	}
}

func TestBrowseSelectAndHelpBar(t *testing.T) {
	m, h, _ := newTestModel(t)
	h.Record(&flow.Flow{ID: 1, Request: flow.Request{Method: "GET", URL: "http://a.test/x", Header: http.Header{}}, Response: &flow.Response{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"k":1}`)}})
	h.Record(&flow.Flow{ID: 2, Request: flow.Request{Method: "POST", URL: "http://b.test/y", Header: http.Header{}}})
	m.Update(tickMsg{})
	key(m, "j")
	out := m.View()
	for _, want := range []string{"b.test", "POST", "quit", "intercept"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
	key(m, "k")
	key(m, "tab")
	if !strings.Contains(m.View(), "200") {
		t.Error("response tab should show status")
	}
}

func TestInterceptToggleForwardAndDrop(t *testing.T) {
	m, _, q := newTestModel(t)
	key(m, "i")
	if !q.Enabled() {
		t.Fatal("intercept not enabled")
	}
	done := make(chan error, 2)
	go func() {
		done <- q.Request(context.Background(), 1, &flow.Request{URL: "http://a.test/", Header: http.Header{}})
	}()
	waitHeld(t, q, 1)
	m.Update(tickMsg{})
	if !strings.Contains(m.View(), "HELD #1") {
		t.Error("held request not shown")
	}
	key(m, "f")
	if err := <-done; err != nil {
		t.Fatalf("forward err = %v", err)
	}
	go func() {
		done <- q.Request(context.Background(), 2, &flow.Request{URL: "http://a.test/", Header: http.Header{}})
	}()
	waitHeld(t, q, 1)
	key(m, "d")
	if err := <-done; err == nil {
		t.Fatal("drop should error")
	}
}

func waitHeld(t *testing.T, q *pipeline.Queue, n int) {
	t.Helper()
	for i := 0; i < 200 && len(q.Pending()) != n; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if len(q.Pending()) != n {
		t.Fatal("hold not reached")
	}
}

func TestSearchFilters(t *testing.T) {
	m, h, _ := newTestModel(t)
	h.Record(&flow.Flow{ID: 1, Request: flow.Request{Method: "GET", URL: "http://a.test/one", Header: http.Header{}}})
	h.Record(&flow.Flow{ID: 2, Request: flow.Request{Method: "GET", URL: "http://a.test/two", Header: http.Header{}}})
	m.Update(tickMsg{})
	key(m, "/")
	for _, r := range "two" {
		key(m, string(r))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.flows) != 1 || m.flows[0].ID != 2 {
		t.Fatalf("flows = %v", m.flows)
	}
}

func TestHighlight(t *testing.T) {
	out := highlightBody(http.Header{"Content-Type": {"application/json"}}, []byte(`{"a":[1,true,null],"s":"x"}`))
	if !strings.Contains(out, "\n") || !strings.Contains(out, "true") {
		t.Fatalf("json not pretty: %q", out)
	}
	if got := highlightBody(nil, []byte{0xff, 0xfe}); !strings.Contains(got, "binary") {
		t.Fatal(got)
	}
	if got := highlightBody(nil, []byte("plain")); got != "plain" {
		t.Fatal(got)
	}
	if !strings.Contains(highlightHeaders(http.Header{"X-A": {"1"}}), "X-A") {
		t.Fatal("header name lost")
	}
}
