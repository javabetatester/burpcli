package repeater

import "testing"

func TestParseRawRequest_PathOnly(t *testing.T) {
	raw := "GET /foo HTTP/1.1\r\nHost: example.com\r\nUser-Agent: x\r\n\r\n"
	req, err := parseRawRequest(raw)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if req.URL.Scheme != "http" {
		t.Fatalf("expected scheme http, got %q", req.URL.Scheme)
	}
	if req.URL.Host != "example.com" {
		t.Fatalf("expected host example.com, got %q", req.URL.Host)
	}
	if req.URL.Path != "/foo" {
		t.Fatalf("expected path /foo, got %q", req.URL.Path)
	}
}

func TestParseRawRequest_AbsoluteURL(t *testing.T) {
	raw := "GET https://example.com/foo HTTP/1.1\r\nUser-Agent: x\r\n\r\n"
	req, err := parseRawRequest(raw)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if req.URL.Scheme != "https" {
		t.Fatalf("expected scheme https, got %q", req.URL.Scheme)
	}
	if req.URL.Host != "example.com" {
		t.Fatalf("expected host example.com, got %q", req.URL.Host)
	}
	if req.URL.Path != "/foo" {
		t.Fatalf("expected path /foo, got %q", req.URL.Path)
	}
}
