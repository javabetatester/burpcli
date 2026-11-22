package flow

import (
	"net/http"
	"testing"
)

func TestRawRoundTrip(t *testing.T) {
	in := Request{Method: "POST", URL: "https://example.test/a?b=1", Proto: "HTTP/1.1", Header: http.Header{"Content-Type": {"application/json"}, "X-A": {"1", "2"}}, Body: []byte("{\"a\":1}\n\nmore")}
	out, err := ParseRaw(in.Raw())
	if err != nil {
		t.Fatal(err)
	}
	if out.Method != in.Method || out.URL != in.URL || string(out.Body) != string(in.Body) || len(out.Header["X-A"]) != 2 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestParseRawErrors(t *testing.T) {
	for _, s := range []string{"", "GET", "GET /relative HTTP/1.1\n\n", "GET ftp://x/ HTTP/1.1\n\n", "GET http://x/ HTTP/1.1\nbadheader\n\n"} {
		if _, err := ParseRaw(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

func TestHost(t *testing.T) {
	r := Request{URL: "http://a.test:8080/x"}
	if r.Host() != "a.test:8080" || r.Hostname() != "a.test" {
		t.Fatal(r.Host(), r.Hostname())
	}
}
