package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	ListenAddr   string
	MaxBodyBytes int
}

type Proxy struct {
	cfg    Config
	ctrl   *Controller
	flowCh chan<- *FlowSnapshot

	server    *http.Server
	transport *http.Transport
}

type readerCloser struct {
	io.Reader
	io.Closer
}

func New(cfg Config, ctrl *Controller, flowCh chan<- *FlowSnapshot) *Proxy {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	p := &Proxy{cfg: cfg, ctrl: ctrl, flowCh: flowCh, transport: tr}
	p.server = &http.Server{Addr: cfg.ListenAddr, Handler: p}
	return p
}

func (p *Proxy) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- p.server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.server.Shutdown(shutdownCtx)
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}

	p.handleHTTP(w, r)
}

func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	flow := newFlow()
	flow.Method = r.Method
	flow.Host = r.Host
	flow.URL = requestURLString(r)
	flow.RequestHeader = cloneHeader(r.Header)

	p.emit(flow)

	if p.ctrl.InterceptEnabled() {
		flow.Intercepted = true
		flow.Pending = true
		p.emit(flow)
		decision := flow.waitDecision()
		if decision == DecisionDrop {
			flow.Error = "dropped"
			flow.Pending = false
			flow.Duration = time.Since(flow.StartedAt)
			p.emit(flow)
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("dropped\n"))
			return
		}
	}

	r.Close = false
	outgoingURL := cloneURL(r.URL)
	if outgoingURL.Scheme == "" {
		outgoingURL.Scheme = "http"
	}
	if outgoingURL.Host == "" {
		outgoingURL.Host = r.Host
	}

	lb := NewLimitBuffer(p.cfg.MaxBodyBytes)
	var body io.ReadCloser
	if r.Body != nil {
		tee := io.TeeReader(r.Body, lb)
		body = readerCloser{Reader: tee, Closer: r.Body}
	}

	outReq := &http.Request{}
	*outReq = *r
	outReq.URL = outgoingURL
	outReq.RequestURI = ""
	outReq.Header = cleanHopByHopHeaders(cloneHeader(r.Header))
	outReq.Body = body
	outReq.Host = r.Host

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		flow.Error = err.Error()
		flow.Pending = false
		flow.Duration = time.Since(flow.StartedAt)
		flow.RequestBody = lb.Bytes()
		flow.ReqTruncated = lb.Truncated
		p.emit(flow)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway\n"))
		return
	}
	defer resp.Body.Close()

	flow.RequestBody = lb.Bytes()
	flow.ReqTruncated = lb.Truncated
	flow.StatusCode = resp.StatusCode
	flow.ResponseHeader = cloneHeader(resp.Header)

	for k, vv := range cleanHopByHopHeaders(cloneHeader(resp.Header)) {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	respLB := NewLimitBuffer(p.cfg.MaxBodyBytes)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			_, _ = respLB.Write(buf[:n])
			_, _ = w.Write(buf[:n])
		}
		if readErr != nil {
			if readErr != io.EOF {
				flow.Error = readErr.Error()
			}
			break
		}
	}

	flow.ResponseBody = respLB.Bytes()
	flow.RespTruncated = respLB.Truncated
	flow.Pending = false
	flow.Duration = time.Since(flow.StartedAt)
	p.emit(flow)
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	targetConn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}

	clientConn, buf, err := hijacker.Hijack()
	if err != nil {
		_ = targetConn.Close()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		_ = copyAndClose(targetConn, buf)
	}()
	_ = copyAndClose(clientConn, targetConn)
}

func copyAndClose(dst io.WriteCloser, src io.Reader) error {
	_, err := io.Copy(dst, src)
	_ = dst.Close()
	return err
}

func (p *Proxy) emit(flow *Flow) {
	snap := &FlowSnapshot{Flow: cloneFlow(flow)}
	select {
	case p.flowCh <- snap:
	default:
	}
}

func cloneFlow(f *Flow) *Flow {
	c := *f
	c.RequestHeader = cloneHeader(f.RequestHeader)
	c.ResponseHeader = cloneHeader(f.ResponseHeader)
	if f.RequestBody != nil {
		c.RequestBody = append([]byte(nil), f.RequestBody...)
	}
	if f.ResponseBody != nil {
		c.ResponseBody = append([]byte(nil), f.ResponseBody...)
	}
	return &c
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	c := make(http.Header, len(h))
	for k, v := range h {
		vv := make([]string, len(v))
		copy(vv, v)
		c[k] = vv
	}
	return c
}

func requestURLString(r *http.Request) string {
	if r.URL == nil {
		return ""
	}
	if r.URL.IsAbs() {
		return r.URL.String()
	}
	u := cloneURL(r.URL)
	u.Scheme = "http"
	u.Host = r.Host
	return u.String()
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return &url.URL{}
	}
	c := *u
	if u.User != nil {
		user := *u.User
		c.User = &user
	}
	return &c
}

func cleanHopByHopHeaders(h http.Header) http.Header {
	if h == nil {
		return nil
	}

	if c := h.Get("Connection"); c != "" {
		parts := strings.Split(c, ",")
		for _, p := range parts {
			if name := strings.TrimSpace(p); name != "" {
				h.Del(name)
			}
		}
	}

	h.Del("Connection")
	h.Del("Proxy-Connection")
	h.Del("Keep-Alive")
	h.Del("Proxy-Authenticate")
	h.Del("Proxy-Authorization")
	h.Del("Te")
	h.Del("Trailer")
	h.Del("Transfer-Encoding")
	h.Del("Upgrade")

	return h
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func parseHostPort(hostport string) (string, string, error) {
	if strings.Contains(hostport, ":") {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil {
			return "", "", err
		}
		return host, port, nil
	}
	return hostport, "", nil
}

func formatAddr(host, port string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("host vazio")
	}
	if port == "" {
		port = "80"
	}
	return net.JoinHostPort(host, port), nil
}
