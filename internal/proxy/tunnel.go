package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/javabetatester/burpcli/internal/flow"
)

const (
	tlsHandshakeByte = 0x16
	peekWait         = 3 * time.Second
)

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy: hijack unsupported", http.StatusInternalServerError)
		return
	}
	f := &flow.Flow{ID: s.hist.NextID(), Started: time.Now(), Tunnel: true,
		Request: flow.Request{Method: http.MethodConnect, URL: "https://" + target, Proto: r.Proto, Header: http.Header{}}}
	host, _, _ := net.SplitHostPort(target)
	var upstream net.Conn
	if s.ca == nil {
		var err error
		if upstream, err = (&net.Dialer{Timeout: dialTimeout}).DialContext(r.Context(), "tcp", target); err != nil {
			s.fail(w, f, http.StatusBadGateway, err)
			return
		}
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		s.log.Error("hijack", "err", err)
		return
	}
	defer client.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	f.Response = &flow.Response{Status: http.StatusOK, Proto: "HTTP/1.1"}
	if s.ca != nil {
		_ = client.SetReadDeadline(time.Now().Add(peekWait))
		first, err := rw.Reader.Peek(1)
		_ = client.SetReadDeadline(time.Time{})
		if err == nil && first[0] == tlsHandshakeByte {
			f.Tunnel = false
			s.record(f)
			s.mitm(&bufferedConn{Conn: client, r: rw.Reader}, host, target)
			return
		}
		if upstream, err = (&net.Dialer{Timeout: dialTimeout}).DialContext(context.Background(), "tcp", target); err != nil {
			f.Error = err.Error()
			s.record(f)
			return
		}
	}
	defer upstream.Close()
	pipe(upstream, &bufferedConn{Conn: client, r: rw.Reader})
	s.record(f)
}

func (s *Server) mitm(conn net.Conn, host, target string) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := chi.ServerName
			if name == "" {
				name = host
			}
			return s.ca.Leaf(name)
		},
	}
	tc := tls.Server(conn, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(ctx); err != nil {
		s.log.Debug("mitm handshake", "host", host, "err", err)
		return
	}
	l := newConnListener(tc)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := url.Parse("https://" + strings.TrimSuffix(target, ":443") + r.RequestURI)
		if err != nil {
			http.Error(w, "proxy: bad request URI", http.StatusBadRequest)
			return
		}
		r.URL = u
		s.exchange(w, r)
	})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 15 * time.Second}
	if err := srv.Serve(l); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
		s.log.Debug("mitm serve", "host", host, "err", err)
	}
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request, f *flow.Flow, req flow.Request) {
	f.Request, f.WebSocket = req, true
	up, err := s.dial(r.Context(), r.URL)
	if err != nil {
		s.fail(w, f, http.StatusBadGateway, err)
		return
	}
	defer up.Close()
	if err := r.Write(up); err != nil {
		s.fail(w, f, http.StatusBadGateway, err)
		return
	}
	upr := bufio.NewReader(up)
	res, err := http.ReadResponse(upr, r)
	if err != nil {
		s.fail(w, f, http.StatusBadGateway, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.fail(w, f, http.StatusInternalServerError, errors.New("hijack unsupported"))
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	f.Response = &flow.Response{Status: res.StatusCode, Proto: res.Proto, Header: res.Header.Clone()}
	s.record(f)
	if err := res.Write(client); err != nil || res.StatusCode != http.StatusSwitchingProtocols {
		return
	}
	pipe(&bufferedConn{Conn: up, r: upr}, &bufferedConn{Conn: client, r: rw.Reader})
}

func (s *Server) dial(ctx context.Context, u *url.URL) (net.Conn, error) {
	addr := hostPort(u)
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && u.Scheme != "wss" {
		return conn, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.upstreamTLS != nil {
		cfg = s.upstreamTLS.Clone()
	}
	cfg.ServerName = u.Hostname()
	cfg.NextProtos = []string{"http/1.1"}
	tc := tls.Client(conn, cfg)
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

func pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

type connListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
	addr net.Addr
}

type notifyConn struct {
	net.Conn
	onClose func()
}

func (c *notifyConn) Close() error {
	c.onClose()
	return c.Conn.Close()
}

func newConnListener(c net.Conn) *connListener {
	l := &connListener{ch: make(chan net.Conn, 1), done: make(chan struct{}), addr: c.LocalAddr()}
	l.ch <- &notifyConn{Conn: c, onClose: func() { _ = l.Close() }}
	return l
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return l.addr }
