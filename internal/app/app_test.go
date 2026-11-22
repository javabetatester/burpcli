package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestHeadlessEmitsJSONLines(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer up.Close()
	addr := freeAddr(t)
	out, errOut := &syncBuf{}, &syncBuf{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{Listen: addr, Headless: true, MITM: false, CADir: t.TempDir(), HistorySize: 10, HoldTimeout: time.Second, Stdout: out, Stderr: errOut})
	}()
	pu, _ := url.Parse("http://" + addr)
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	var resp *http.Response
	var err error
	for i := 0; i < 100; i++ {
		if resp, err = c.Get(up.URL + "/hi"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var rec Record
	for i := 0; i < 100 && out.String() == ""; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if err := json.Unmarshal([]byte(strings.Split(out.String(), "\n")[0]), &rec); err != nil {
		t.Fatalf("stdout %q: %v", out.String(), err)
	}
	if rec.Schema != RecordSchema || rec.Method != "GET" || rec.Status != 200 || rec.URL != up.URL+"/hi" {
		t.Fatalf("record = %+v", rec)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}
