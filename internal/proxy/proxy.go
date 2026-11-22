// Package proxy implements the intercepting HTTP/1.1 forward proxy with CONNECT, HTTPS interception and WebSocket passthrough.
package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/javabetatester/burpcli/internal/ca"
	"github.com/javabetatester/burpcli/internal/flow"
	"github.com/javabetatester/burpcli/internal/history"
	"github.com/javabetatester/burpcli/internal/pipeline"
)

// ErrBodyTooLarge reports a body above the configured buffering limit.
var ErrBodyTooLarge = errors.New("proxy: body exceeds buffer limit")

const (
	defaultMaxBody    = 32 << 20
	defaultMaxCapture = 4 << 20
	dialTimeout       = 10 * time.Second
	handshakeTimeout  = 10 * time.Second
)

// Config wires the proxy to its collaborators.
type Config struct {
	Transport   http.RoundTripper
	UpstreamTLS *tls.Config
	CA          *ca.Authority
	Chain       pipeline.Interceptor
	History     *history.Store
	MaxBody     int64
	MaxCapture  int
	Log         *slog.Logger
}

// Server is an http.Handler implementing the proxy.
type Server struct {
	transport   http.RoundTripper
	upstreamTLS *tls.Config
	ca          *ca.Authority
	chain       pipeline.Interceptor
	hist        *history.Store
	maxBody     int64
	maxCapture  int
	log         *slog.Logger
}

// New validates cfg and builds a Server. Without a CA, CONNECT is tunnelled blindly.
func New(cfg Config) (*Server, error) {
	if cfg.History == nil {
		return nil, errors.New("proxy: history store required")
	}
	s := &Server{transport: cfg.Transport, upstreamTLS: cfg.UpstreamTLS, ca: cfg.CA, chain: cfg.Chain, hist: cfg.History,
		maxBody: cfg.MaxBody, maxCapture: cfg.MaxCapture, log: cfg.Log}
	if s.transport == nil {
		s.transport = NewTransport(cfg.UpstreamTLS)
	}
	if s.chain == nil {
		s.chain = pipeline.Chain{}
	}
	if s.maxBody <= 0 {
		s.maxBody = defaultMaxBody
	}
	if s.maxCapture <= 0 {
		s.maxCapture = defaultMaxCapture
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	return s, nil
}

// NewTransport builds the upstream transport: TLS verified by tlsCfg (system roots when nil), HTTP/2 when negotiated, no env proxy.
func NewTransport(tlsCfg *tls.Config) *http.Transport {
	return &http.Transport{
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSHandshakeTimeout:   handshakeTimeout,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	}
}

// Serve accepts connections on l until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 15 * time.Second}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		case <-stopped:
		}
	}()
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("proxy: serve: %w", err)
	}
	return nil
}

// ServeHTTP handles proxy requests (absolute-form URLs) and CONNECT.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		s.connect(w, r)
	case !r.URL.IsAbs():
		http.Error(w, "proxy: absolute-form request URI required", http.StatusBadRequest)
	default:
		s.exchange(w, r)
	}
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := &flow.Flow{ID: s.hist.NextID(), Started: time.Now()}
	req, err := readRequest(r, s.maxBody)
	if err != nil {
		f.Request = req
		s.fail(w, f, http.StatusRequestEntityTooLarge, err)
		return
	}
	if isUpgrade(r) {
		s.websocket(w, r, f, req)
		return
	}
	if err := s.chain.Request(ctx, f.ID, &req); err != nil {
		f.Request = req
		f.Dropped = errors.Is(err, pipeline.ErrDropped)
		status := http.StatusBadGateway
		if f.Dropped {
			status = http.StatusForbidden
		}
		s.fail(w, f, status, err)
		return
	}
	f.Request = req
	resp, err := s.roundTrip(ctx, req)
	if err == nil {
		err = s.chain.Response(ctx, f.ID, &resp)
	}
	if err != nil {
		s.fail(w, f, http.StatusBadGateway, err)
		return
	}
	f.Response = &resp
	writeResponse(w, req.Method, resp)
	s.record(f)
}

func (s *Server) fail(w http.ResponseWriter, f *flow.Flow, status int, err error) {
	f.Error = err.Error()
	s.log.Debug("exchange failed", "id", f.ID, "url", f.Request.URL, "err", err)
	http.Error(w, "burpui: "+err.Error(), status)
	s.record(f)
}

func (s *Server) record(f *flow.Flow) {
	f.Duration = time.Since(f.Started)
	if len(f.Request.Body) > s.maxCapture {
		f.Request.Body, f.Truncated = f.Request.Body[:s.maxCapture], true
	}
	if f.Response != nil && len(f.Response.Body) > s.maxCapture {
		f.Response.Body, f.Truncated = f.Response.Body[:s.maxCapture], true
	}
	s.hist.Record(f)
}

func (s *Server) roundTrip(ctx context.Context, req flow.Request) (flow.Response, error) {
	var body io.Reader = http.NoBody
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	out, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return flow.Response{}, fmt.Errorf("proxy: build upstream request: %w", err)
	}
	out.Header = req.Header.Clone()
	if h := out.Header.Get("Host"); h != "" {
		out.Host = h
		out.Header.Del("Host")
	}
	out.ContentLength = int64(len(req.Body))
	res, err := s.transport.RoundTrip(out)
	if err != nil {
		return flow.Response{}, fmt.Errorf("proxy: upstream: %w", err)
	}
	defer res.Body.Close()
	data, err := readLimited(res.Body, s.maxBody)
	if err != nil {
		return flow.Response{}, err
	}
	h := res.Header.Clone()
	stripHopByHop(h)
	return flow.Response{Status: res.StatusCode, Proto: res.Proto, Header: h, Body: data}, nil
}

func readRequest(r *http.Request, limit int64) (flow.Request, error) {
	req := flow.Request{Method: r.Method, URL: r.URL.String(), Proto: r.Proto, Header: r.Header.Clone()}
	if isUpgrade(r) {
		return req, nil
	}
	stripHopByHop(req.Header)
	if r.Host != "" {
		req.Header.Set("Host", r.Host)
	}
	body, err := readLimited(r.Body, limit)
	req.Body = body
	return req, err
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("proxy: read body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}

func writeResponse(w http.ResponseWriter, method string, resp flow.Response) {
	h := w.Header()
	for k, vs := range resp.Header {
		h[k] = vs
	}
	h.Del("Transfer-Encoding")
	if method != http.MethodHead {
		h.Set("Content-Length", fmt.Sprint(len(resp.Body)))
	}
	w.WriteHeader(resp.Status)
	if method != http.MethodHead {
		_, _ = w.Write(resp.Body)
	}
}

var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func stripHopByHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for _, k := range hopByHop {
		h.Del(k)
	}
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && headerHasToken(r.Header, "Connection", "upgrade")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	if u.Scheme == "https" || u.Scheme == "wss" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return net.JoinHostPort(u.Hostname(), "80")
}
