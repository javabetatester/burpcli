package har

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/javabetatester/burpcli/internal/flow"
)

var update = flag.Bool("update", false, "rewrite golden files")

func sample() []*flow.Flow {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	return []*flow.Flow{
		{ID: 1, Started: t0, Duration: 150 * time.Millisecond,
			Request:  flow.Request{Method: "POST", URL: "https://api.test/v1/items?b=2&a=1", Proto: "HTTP/1.1", Header: http.Header{"Content-Type": {"application/json"}, "X-Z": {"1"}}, Body: []byte(`{"n":1}`)},
			Response: &flow.Response{Status: 201, Proto: "HTTP/2.0", Header: http.Header{"Content-Type": {"application/json"}, "Location": {"/v1/items/9"}}, Body: []byte(`{"id":9}`)}},
		{ID: 2, Started: t0.Add(time.Second), Duration: 5 * time.Millisecond,
			Request:  flow.Request{Method: "GET", URL: "http://cdn.test/img.png", Proto: "HTTP/1.1", Header: http.Header{}},
			Response: &flow.Response{Status: 200, Proto: "HTTP/1.1", Header: http.Header{"Content-Type": {"image/png"}}, Body: []byte{0x89, 'P', 'N', 'G', 0xff}}},
		{ID: 3, Started: t0.Add(2 * time.Second), Request: flow.Request{Method: "GET", URL: "http://down.test/", Proto: "HTTP/1.1", Header: http.Header{}}, Error: "dial failed"},
	}
}

func TestGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "test", sample()); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/golden.har"
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("HAR differs from golden; run go test ./internal/har -update\n%s", buf.String())
	}
}

func TestStructure(t *testing.T) {
	var buf bytes.Buffer
	_ = Write(&buf, "test", sample())
	var doc struct {
		Log struct {
			Version string
			Entries []map[string]any
		}
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Log.Version != "1.2" || len(doc.Log.Entries) != 3 {
		t.Fatalf("doc = %+v", doc.Log)
	}
}
