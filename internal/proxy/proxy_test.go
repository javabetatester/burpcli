package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/javabetatester/burpcli/internal/ca"
	"github.com/javabetatester/burpcli/internal/flow"
	"github.com/javabetatester/burpcli/internal/history"
	"github.com/javabetatester/burpcli/internal/pipeline"
)

type rig struct {
	proxyURL *url.URL
	hist     *history.Store
	queue    *pipeline.Queue
	ca       *ca.Authority
}

type headerTagger struct{ pipeline.Passthrough }

func (headerTagger) Request(_ context.Context, _ uint64, r *flow.Request) error {
	r.Header.Set("X-Tagged", "yes")
	return nil
}

func (headerTagger) Response(_ context.Context, _ uint64, r *flow.Response) error {
	r.Header.Set("X-Resp-Tagged", "yes")
	return nil
}

func newRig(t *testing.T, upstreamTrust *x509.Certificate, withCA bool, mutate func(*Config)) *rig {
	t.Helper()
	hist, err := history.New(100, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := pipeline.NewQueue(300*time.Millisecond, nil)
	cfg := Config{History: hist, Chain: pipeline.Chain{headerTagger{}, q}, MaxBody: 1 << 20}
	if upstreamTrust != nil {
		pool := x509.NewCertPool()
		pool.AddCert(upstreamTrust)
		cfg.UpstreamTLS = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	r := &rig{hist: hist, queue: q}
	if withCA {
		if r.ca, err = ca.Generate(); err != nil {
			t.Fatal(err)
		}
		cfg.CA = r.ca
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	r.proxyURL = &url.URL{Scheme: "http", Host: l.Addr().String()}
	return r
}

func (r *rig) client(roots *x509.CertPool) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: http.ProxyURL(r.proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestForwardHTTPRunsChainAndRecords(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Seen-Tag", r.Header.Get("X-Tagged"))
		w.Header().Set("Set-Cookie", "a=b")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("echo:" + string(b) + ":" + r.Header.Get("Proxy-Connection")))
	}))
	defer up.Close()
	r := newRig(t, nil, false, nil)
	req, _ := http.NewRequest("POST", up.URL+"/p?q=1", strings.NewReader("payload"))
	req.Header.Set("Proxy-Connection", "keep-alive")
	resp, err := r.client(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "echo:payload:" {
		t.Fatalf("body = %q", got)
	}
	if resp.StatusCode != 201 || resp.Header.Get("X-Seen-Tag") != "yes" || resp.Header.Get("X-Resp-Tagged") != "yes" {
		t.Fatalf("resp = %d %v", resp.StatusCode, resp.Header)
	}
	flows := r.hist.List(history.Filter{})
	if len(flows) != 1 {
		t.Fatalf("flows = %d", len(flows))
	}
	f := flows[0]
	if f.Request.Method != "POST" || f.Request.URL != up.URL+"/p?q=1" || string(f.Request.Body) != "payload" || f.Response.Status != 201 || f.Duration <= 0 {
		t.Fatalf("flow = %+v", f)
	}
}

func TestRejectsNonAbsoluteRequest(t *testing.T) {
	r := newRig(t, nil, false, nil)
	resp, err := http.Get(r.proxyURL.String() + "/relative")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestBodyTooLarge(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	r := newRig(t, nil, false, func(c *Config) { c.MaxBody = 10 })
	resp, err := r.client(nil).Post(up.URL, "text/plain", strings.NewReader(strings.Repeat("x", 100)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func waitPending(t *testing.T, q *pipeline.Queue) pipeline.Held {
	t.Helper()
	for i := 0; i < 200; i++ {
		if p := q.Pending(); len(p) > 0 {
			return p[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nothing held")
	return pipeline.Held{}
}

func TestInterceptEditForwardDropAndTimeout(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer up.Close()
	r := newRig(t, nil, false, nil)
	r.queue.SetEnabled(true)
	c := r.client(nil)

	type result struct {
		resp *http.Response
		err  error
	}
	get := func(path string) chan result {
		ch := make(chan result, 1)
		go func() {
			resp, err := c.Get(up.URL + path)
			ch <- result{resp, err}
		}()
		return ch
	}

	ch := get("/original")
	held := waitPending(t, r.queue)
	edited := held.Request.Clone()
	edited.URL = up.URL + "/edited"
	if err := r.queue.Resolve(held.ID, pipeline.Decision{Action: pipeline.Forward, Edited: &edited}); err != nil {
		t.Fatal(err)
	}
	res := <-ch
	if res.err != nil || body(t, res.resp) != "/edited" {
		t.Fatalf("edit forward failed: %v", res.err)
	}

	ch = get("/dropme")
	held = waitPending(t, r.queue)
	_ = r.queue.Resolve(held.ID, pipeline.Decision{Action: pipeline.Drop})
	res = <-ch
	if res.err != nil || res.resp.StatusCode != http.StatusForbidden {
		t.Fatalf("drop: %v %v", res.err, res.resp)
	}
	res.resp.Body.Close()

	start := time.Now()
	resp, err := c.Get(up.URL + "/timeout")
	if err != nil || resp.StatusCode != http.StatusForbidden || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout: %v %v", err, resp)
	}
	resp.Body.Close()
	var dropped int
	for _, f := range r.hist.List(history.Filter{}) {
		if f.Dropped {
			dropped++
		}
	}
	if dropped != 2 {
		t.Fatalf("dropped flows = %d", dropped)
	}
}

func TestMITMHTTPSWithHTTP2Upstream(t *testing.T) {
	var upProto int
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upProto = r.ProtoMajor
		_, _ = w.Write([]byte("secret-" + r.Header.Get("X-Tagged")))
	}))
	up.EnableHTTP2 = true
	up.StartTLS()
	defer up.Close()
	r := newRig(t, up.Certificate(), true, nil)
	resp, err := r.client(r.ca.Pool()).Get(up.URL + "/s?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "secret-yes" {
		t.Fatalf("body = %q", got)
	}
	if resp.TLS == nil || resp.TLS.PeerCertificates[0].Issuer.CommonName != "burpui local CA" {
		t.Fatal("client did not receive a CA-minted leaf")
	}
	if upProto != 2 {
		t.Fatalf("upstream proto major = %d", upProto)
	}
	var seen *flow.Flow
	for _, f := range r.hist.List(history.Filter{}) {
		if f.Request.Method == "GET" {
			seen = f
		}
	}
	if seen == nil || seen.Request.URL != up.URL+"/s?x=1" || seen.Response.Proto != "HTTP/2.0" || string(seen.Response.Body) != "secret-yes" {
		t.Fatalf("flow = %+v", seen)
	}
}

func TestUpstreamTLSVerifiedByDefault(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("no")) }))
	defer up.Close()
	r := newRig(t, nil, true, nil)
	resp, err := r.client(r.ca.Pool()).Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for unverifiable upstream", resp.StatusCode)
	}
}

func TestConnectTunnelWithoutCA(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("direct")) }))
	defer up.Close()
	r := newRig(t, nil, false, nil)
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	resp, err := r.client(pool).Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	if body(t, resp) != "direct" || resp.TLS.PeerCertificates[0].Issuer.CommonName == "burpui local CA" {
		t.Fatal("tunnel should expose the real upstream certificate")
	}
}

func TestWebSocketUpgradePassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		line, _ := rw.ReadString('\n')
		_, _ = rw.WriteString("echo " + line)
		_ = rw.Flush()
	}))
	defer up.Close()
	r := newRig(t, nil, false, nil)
	conn, err := net.Dial("tcp", r.proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	u, _ := url.Parse(up.URL)
	_, _ = io.WriteString(conn, "GET "+up.URL+"/ws HTTP/1.1\r\nHost: "+u.Host+"\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("handshake: %v %v", err, resp)
	}
	_, _ = io.WriteString(conn, "ping\n")
	line, err := br.ReadString('\n')
	if err != nil || line != "echo ping\n" {
		t.Fatalf("echo = %q err=%v", line, err)
	}
	found := false
	for _, f := range r.hist.List(history.Filter{}) {
		found = found || (f.WebSocket && f.Response != nil && f.Response.Status == 101)
	}
	if !found {
		t.Fatal("websocket flow not recorded")
	}
}
