// Package flow defines the records that travel through the proxy pipeline.
package flow

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// ErrBadRaw reports a raw request text that cannot be parsed.
var ErrBadRaw = errors.New("flow: malformed raw request")

// Request is a buffered HTTP request.
type Request struct {
	Method string      `json:"method"`
	URL    string      `json:"url"`
	Proto  string      `json:"proto"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body,omitempty"`
}

// Response is a buffered HTTP response.
type Response struct {
	Status int         `json:"status"`
	Proto  string      `json:"proto"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body,omitempty"`
}

// Flow is one proxied exchange. Recorded flows are immutable.
type Flow struct {
	ID        uint64        `json:"id"`
	Started   time.Time     `json:"started"`
	Duration  time.Duration `json:"duration"`
	Request   Request       `json:"request"`
	Response  *Response     `json:"response,omitempty"`
	Error     string        `json:"error,omitempty"`
	Dropped   bool          `json:"dropped,omitempty"`
	WebSocket bool          `json:"websocket,omitempty"`
	Tunnel    bool          `json:"tunnel,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
}

// Clone returns a deep copy of the request.
func (r Request) Clone() Request {
	r.Header = r.Header.Clone()
	r.Body = append([]byte(nil), r.Body...)
	return r
}

// Clone returns a deep copy of the response.
func (r Response) Clone() Response {
	r.Header = r.Header.Clone()
	r.Body = append([]byte(nil), r.Body...)
	return r
}

// Host returns the host:port of the request URL, or an empty string.
func (r Request) Host() string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return ""
	}
	return u.Host
}

// Hostname returns the request host without a port.
func (r Request) Hostname() string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Raw renders the request as editable text: request line, headers, blank line, body.
func (r Request) Raw() string {
	var b strings.Builder
	b.WriteString(r.Method + " " + r.URL + " " + r.Proto + "\n")
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		for _, v := range r.Header[k] {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	b.WriteString("\n")
	b.Write(r.Body)
	return b.String()
}

// ParseRaw is the inverse of Request.Raw.
func ParseRaw(s string) (Request, error) {
	head, body, _ := strings.Cut(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n")
	lines := strings.Split(head, "\n")
	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return Request{}, ErrBadRaw
	}
	u, err := url.Parse(parts[1])
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Request{}, ErrBadRaw
	}
	req := Request{Method: strings.ToUpper(parts[0]), URL: parts[1], Proto: "HTTP/1.1", Header: http.Header{}, Body: []byte(body)}
	if len(parts) > 2 {
		req.Proto = parts[2]
	}
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return Request{}, ErrBadRaw
		}
		req.Header.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return req, nil
}
