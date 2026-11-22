package repeater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/javabetatester/burpcli/internal/flow"
)

func TestSendEditedRequest(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Method", r.Method)
		_, _ = w.Write([]byte(r.Header.Get("X-Edit") + r.Host))
	}))
	defer up.Close()
	req := flow.Request{Method: "GET", URL: up.URL, Header: http.Header{"X-Edit": {"v2"}, "Host": {"virtual.test"}}}
	resp, d, err := New(http.DefaultTransport).Send(context.Background(), req)
	if err != nil || d <= 0 {
		t.Fatal(err, d)
	}
	if string(resp.Body) != "v2virtual.test" || resp.Header.Get("X-Method") != "GET" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSendError(t *testing.T) {
	if _, _, err := New(http.DefaultTransport).Send(context.Background(), flow.Request{Method: "GET", URL: "http://127.0.0.1:1/"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestDiff(t *testing.T) {
	a := flow.Response{Status: 200, Proto: "HTTP/1.1", Header: http.Header{"A": {"1"}}, Body: []byte("one\ntwo")}
	b := flow.Response{Status: 200, Proto: "HTTP/1.1", Header: http.Header{"A": {"2"}}, Body: []byte("one\nthree")}
	got := Diff(a, b)
	for _, want := range []string{"-A: 1", "+A: 2", " one", "-two", "+three", " HTTP/1.1 200 OK"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff missing %q:\n%s", want, got)
		}
	}
	for _, l := range strings.Split(Diff(a, a), "\n") {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			t.Errorf("identical responses produced change %q", l)
		}
	}
}
