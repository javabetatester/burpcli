// Package repeater resends a stored request with edits and diffs the responses.
package repeater

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/javabetatester/burpcli/internal/flow"
)

const maxBody = 32 << 20

// Repeater sends requests through a RoundTripper.
type Repeater struct{ rt http.RoundTripper }

// New builds a Repeater over the given upstream transport.
func New(rt http.RoundTripper) *Repeater { return &Repeater{rt: rt} }

// Send performs req and returns the buffered response.
func (r *Repeater) Send(ctx context.Context, req flow.Request) (flow.Response, time.Duration, error) {
	var body io.Reader = http.NoBody
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	out, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return flow.Response{}, 0, fmt.Errorf("repeater: build request: %w", err)
	}
	out.Header = req.Header.Clone()
	if h := out.Header.Get("Host"); h != "" {
		out.Host = h
		out.Header.Del("Host")
	}
	out.ContentLength = int64(len(req.Body))
	start := time.Now()
	res, err := r.rt.RoundTrip(out)
	if err != nil {
		return flow.Response{}, 0, fmt.Errorf("repeater: send: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return flow.Response{}, 0, fmt.Errorf("repeater: read body: %w", err)
	}
	return flow.Response{Status: res.StatusCode, Proto: res.Proto, Header: res.Header.Clone(), Body: data}, time.Since(start), nil
}

// Render prints a response as status line, sorted headers and body.
func Render(r flow.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %d %s\n", r.Proto, r.Status, http.StatusText(r.Status))
	keys := make([]string, 0, len(r.Header))
	for k := range r.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range r.Header[k] {
			b.WriteString(k + ": " + v + "\n")
		}
	}
	b.WriteString("\n")
	b.Write(r.Body)
	return b.String()
}

// Diff returns a line diff of two responses: "-" only in old, "+" only in new, " " common.
func Diff(old, updated flow.Response) string {
	return diffLines(strings.Split(Render(old), "\n"), strings.Split(Render(updated), "\n"))
}

func diffLines(a, b []string) string {
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, " "+a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "-"+a[i])
			i++
		default:
			out = append(out, "+"+b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, "-"+a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, "+"+b[j])
	}
	return strings.Join(out, "\n")
}
