// Package har exports flows as HTTP Archive 1.2.
package har

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/javabetatester/burpcli/internal/flow"
)

type pair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type file struct {
	Log log `json:"log"`
}

type log struct {
	Version string  `json:"version"`
	Creator creator `json:"creator"`
	Entries []entry `json:"entries"`
}

type creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type entry struct {
	Started  string   `json:"startedDateTime"`
	Time     float64  `json:"time"`
	Request  request  `json:"request"`
	Response response `json:"response"`
	Cache    struct{} `json:"cache"`
	Timings  timings  `json:"timings"`
}

type request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Cookies     []pair    `json:"cookies"`
	Headers     []pair    `json:"headers"`
	QueryString []pair    `json:"queryString"`
	PostData    *postData `json:"postData,omitempty"`
	HeadersSize int       `json:"headersSize"`
	BodySize    int       `json:"bodySize"`
}

type postData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

type response struct {
	Status      int     `json:"status"`
	StatusText  string  `json:"statusText"`
	HTTPVersion string  `json:"httpVersion"`
	Cookies     []pair  `json:"cookies"`
	Headers     []pair  `json:"headers"`
	Content     content `json:"content"`
	RedirectURL string  `json:"redirectURL"`
	HeadersSize int     `json:"headersSize"`
	BodySize    int     `json:"bodySize"`
}

type content struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

type timings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

// Write encodes flows as a HAR 1.2 document, headers sorted for stable output.
func Write(w io.Writer, version string, flows []*flow.Flow) error {
	doc := file{Log: log{Version: "1.2", Creator: creator{Name: "burpui", Version: version}, Entries: make([]entry, 0, len(flows))}}
	for _, f := range flows {
		doc.Log.Entries = append(doc.Log.Entries, toEntry(f))
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("har: encode: %w", err)
	}
	return nil
}

func toEntry(f *flow.Flow) entry {
	ms := float64(f.Duration) / float64(time.Millisecond)
	e := entry{
		Started: f.Started.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Time:    ms,
		Request: toRequest(f.Request),
		Timings: timings{Send: 0, Wait: ms, Receive: 0},
	}
	if f.Response != nil {
		e.Response = toResponse(*f.Response)
	} else {
		e.Response = response{HTTPVersion: "HTTP/1.1", Cookies: []pair{}, Headers: []pair{}, HeadersSize: -1, BodySize: -1, Content: content{MimeType: "x-unknown"}}
	}
	return e
}

func toRequest(r flow.Request) request {
	out := request{Method: r.Method, URL: r.URL, HTTPVersion: r.Proto, Cookies: []pair{}, Headers: pairs(r.Header), QueryString: []pair{}, HeadersSize: -1, BodySize: len(r.Body)}
	if u, err := url.Parse(r.URL); err == nil {
		q := u.Query()
		for _, k := range sortedKeys(q) {
			for _, v := range q[k] {
				out.QueryString = append(out.QueryString, pair{k, v})
			}
		}
	}
	if len(r.Body) > 0 {
		out.PostData = &postData{MimeType: r.Header.Get("Content-Type"), Text: string(r.Body)}
	}
	return out
}

func toResponse(r flow.Response) response {
	c := content{Size: len(r.Body), MimeType: r.Header.Get("Content-Type")}
	if utf8.Valid(r.Body) {
		c.Text = string(r.Body)
	} else {
		c.Text, c.Encoding = base64.StdEncoding.EncodeToString(r.Body), "base64"
	}
	return response{
		Status: r.Status, StatusText: http.StatusText(r.Status), HTTPVersion: r.Proto, Cookies: []pair{}, Headers: pairs(r.Header),
		Content: c, RedirectURL: r.Header.Get("Location"), HeadersSize: -1, BodySize: len(r.Body),
	}
}

func pairs(h http.Header) []pair {
	out := []pair{}
	for _, k := range sortedKeys(h) {
		for _, v := range h[k] {
			out = append(out, pair{k, v})
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
