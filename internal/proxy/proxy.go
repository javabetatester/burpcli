package proxy

import (
	"bufio"
	"bytes"
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

	"burpui/internal/ca"
	"burpui/internal/httpraw"
)

type Config struct {
	ListenAddr   string
	MaxBodyBytes int
	MITM         bool
	CADir        string
}

type Proxy struct {
	cfg    Config
	ctrl   *Controller
	flowCh chan<- *FlowSnapshot

	server    *http.Server
	transport *http.Transport
	ca        *ca.Store
}

type readerCloser struct {
	io.Reader
	io.Closer
}

func New(cfg Config, ctrl *Controller, flowCh chan<- *FlowSnapshot) (*Proxy, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	p := &Proxy{cfg: cfg, ctrl: ctrl, flowCh: flowCh, transport: tr}
	if cfg.MITM {
		st, err := ca.LoadOrCreate(cfg.CADir)
		if err != nil {
			return nil, err
		}
		p.ca = st
	}
	p.server = &http.Server{Addr: cfg.ListenAddr, Handler: p}
	return p, nil
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
	if p.cfg.MITM && r.Method != http.MethodConnect && r.URL != nil && r.URL.IsAbs() && r.URL.Scheme == "http" && (r.URL.Host == "burpui.local" || r.URL.Host == "burpui") && (r.URL.Path == "/ca" || r.URL.Path == "/cacert") {
		p.serveCA(w)
		return
	}
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

	shouldBreak := p.ctrl.ShouldBreak(flow.Method, flow.URL, flow.Host)
	wantIntercept := p.ctrl.InterceptEnabled() || shouldBreak
	canEdit := wantIntercept && canBufferRequest(r, p.cfg.MaxBodyBytes)

	if wantIntercept {
		flow.Intercepted = true
		flow.Pending = true
		if canEdit {
			b, err := readBodyAll(r)
			if err != nil {
				flow.Error = err.Error()
				flow.Pending = false
				flow.Duration = time.Since(flow.StartedAt)
				p.emit(flow)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("bad request\n"))
				return
			}
			flow.RequestBody = b
			flow.ReqTruncated = false
			flow.RawRequest = renderRawRequest(flow.Method, flow.URL, flow.Host, flow.RequestHeader, flow.RequestBody)
		}
		p.emit(flow)

		for {
			a := flow.waitAction()
			switch a.Kind {
			case ActionDrop:
				flow.Error = "dropped"
				flow.Pending = false
				flow.Duration = time.Since(flow.StartedAt)
				p.emit(flow)
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte("dropped\n"))
				return
			case ActionForward:
				flow.Pending = false
				p.emit(flow)
				if canEdit {
					outReq := buildOutgoingRequestFromOriginal(r, flow.RequestBody)
					p.sendPreparedRequest(w, outReq, flow)
					return
				}
				p.sendStreamedRequest(w, r, flow)
				return
			case ActionForwardRaw:
				if !canEdit {
					flow.Error = "edição não disponível para esta request"
					p.emit(flow)
					continue
				}

				req, bodyBytes, err := httpraw.ParseRequest(a.RawRequest)
				if err != nil {
					flow.Error = "parse: " + err.Error()
					p.emit(flow)
					continue
				}

				flow.Method = req.Method
				flow.Host = req.Host
				flow.URL = req.URL.String()
				flow.RequestHeader = cloneHeader(req.Header)
				flow.RequestBody = bodyBytes
				flow.ReqTruncated = false
				flow.RawRequest = a.RawRequest
				flow.Error = ""
				flow.Pending = false
				p.emit(flow)

				p.sendPreparedRequest(w, prepareRequestForRoundTrip(req), flow)
				return
			}
		}
	}

	p.sendStreamedRequest(w, r, flow)
}

func (p *Proxy) sendStreamedRequest(w http.ResponseWriter, r *http.Request, flow *Flow) {
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
	outReq = prepareRequestForRoundTrip(outReq)

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
	p.writeResponse(w, resp, flow)
}

func (p *Proxy) sendPreparedRequest(w http.ResponseWriter, outReq *http.Request, flow *Flow) {
	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		flow.Error = err.Error()
		flow.Pending = false
		flow.Duration = time.Since(flow.StartedAt)
		p.emit(flow)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("bad gateway\n"))
		return
	}
	defer resp.Body.Close()

	p.writeResponse(w, resp, flow)
}

func (p *Proxy) writeResponse(w http.ResponseWriter, resp *http.Response, flow *Flow) {
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

func canBufferRequest(r *http.Request, maxBodyBytes int) bool {
	if r.Body == nil {
		return true
	}
	if r.ContentLength < 0 {
		return false
	}
	if maxBodyBytes <= 0 {
		return true
	}
	return int64(maxBodyBytes) >= r.ContentLength
}

func readBodyAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func buildOutgoingRequestFromOriginal(r *http.Request, body []byte) *http.Request {
	r.Close = false
	outgoingURL := cloneURL(r.URL)
	if outgoingURL.Scheme == "" {
		outgoingURL.Scheme = "http"
	}
	if outgoingURL.Host == "" {
		outgoingURL.Host = r.Host
	}

	outReq := &http.Request{}
	*outReq = *r
	outReq.URL = outgoingURL
	outReq.RequestURI = ""
	outReq.Header = cleanHopByHopHeaders(cloneHeader(r.Header))
	outReq.Body = io.NopCloser(bytes.NewReader(body))
	outReq.Host = r.Host
	return prepareRequestForRoundTrip(outReq)
}

func prepareRequestForRoundTrip(req *http.Request) *http.Request {
	if req == nil {
		return req
	}
	req.RequestURI = ""
	req.Header = cleanHopByHopHeaders(cloneHeader(req.Header))
	req.Close = false
	if req.URL != nil && req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL != nil && req.URL.Host == "" {
		req.URL.Host = req.Host
	}
	return req
}

func renderRawRequest(method, urlStr, host string, header http.Header, body []byte) string {
	var b strings.Builder
	if urlStr == "" {
		urlStr = "/"
	}
	b.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, urlStr))
	if host != "" {
		b.WriteString("Host: ")
		b.WriteString(host)
		b.WriteString("\r\n")
	}
	for k, vv := range header {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vv {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\r\n")
	b.Write(body)
	return b.String()
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	if p.cfg.MITM && p.ca != nil {
		p.handleConnectMITM(w, r)
		return
	}
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

func (p *Proxy) serveCA(w http.ResponseWriter) {
	if p.ca == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", "attachment; filename=burpui-ca.pem")
	_, _ = w.Write(p.ca.RootCertPEM())
}

func (p *Proxy) handleConnectMITM(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	clientConn, buf, err := hijacker.Hijack()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	bc := bufferedConn{Conn: clientConn, r: buf}

	host := r.Host
	hostname := host
	if strings.Contains(host, ":") {
		if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
			hostname = h
		}
	}

	certPEM, keyPEM, err := p.ca.LeafCert(hostname)
	if err != nil {
		_ = clientConn.Close()
		return
	}
	leaf, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		_ = clientConn.Close()
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	tlsSrv := tls.Server(bc, &tls.Config{
		Certificates: []tls.Certificate{leaf},
		MinVersion:   tls.VersionTLS12,
	})
	if err := tlsSrv.Handshake(); err != nil {
		_ = tlsSrv.Close()
		return
	}

	br := bufio.NewReader(tlsSrv)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			_ = tlsSrv.Close()
			return
		}

		req.URL.Scheme = "https"
		req.URL.Host = host
		req.RequestURI = ""
		if req.Host == "" {
			req.Host = host
		}

		p.handleMITMRequest(tlsSrv, req, hostname)
	}
}

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

func (p *Proxy) handleMITMRequest(clientConn net.Conn, req *http.Request, hostname string) {
	flow := newFlow()
	flow.Method = req.Method
	flow.Host = hostname
	flow.URL = req.URL.String()
	flow.RequestHeader = cloneHeader(req.Header)

	shouldBreak := p.ctrl.ShouldBreak(flow.Method, flow.URL, flow.Host)
	wantIntercept := p.ctrl.InterceptEnabled() || shouldBreak
	canEdit := wantIntercept && canBufferRequest(req, p.cfg.MaxBodyBytes)

	if wantIntercept {
		flow.Intercepted = true
		flow.Pending = true
		if canEdit {
			b, err := readBodyAll(req)
			if err != nil {
				flow.Error = err.Error()
				flow.Pending = false
				flow.Duration = time.Since(flow.StartedAt)
				p.emit(flow)
				return
			}
			flow.RequestBody = b
			flow.RawRequest = renderRawRequest(flow.Method, flow.URL, flow.Host, flow.RequestHeader, flow.RequestBody)
			req.Body = io.NopCloser(bytes.NewReader(b))
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		}
		p.emit(flow)

		for {
			a := flow.waitAction()
			switch a.Kind {
			case ActionDrop:
				flow.Error = "dropped"
				flow.Pending = false
				flow.Duration = time.Since(flow.StartedAt)
				p.emit(flow)
				return
			case ActionForward:
				flow.Pending = false
				p.emit(flow)
				p.forwardMITM(clientConn, req, flow)
				return
			case ActionForwardRaw:
				if !canEdit {
					flow.Error = "edição não disponível para esta request"
					p.emit(flow)
					continue
				}
				req2, bodyBytes, err := httpraw.ParseRequest(a.RawRequest)
				if err != nil {
					flow.Error = "parse: " + err.Error()
					p.emit(flow)
					continue
				}
				if req2.URL != nil {
					req2.URL.Scheme = "https"
				}
				req2.RequestURI = ""
				flow.Method = req2.Method
				flow.Host = hostname
				flow.URL = req2.URL.String()
				flow.RequestHeader = cloneHeader(req2.Header)
				flow.RequestBody = bodyBytes
				flow.RawRequest = a.RawRequest
				flow.Error = ""
				flow.Pending = false
				p.emit(flow)

				req2.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				req2.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(bodyBytes)), nil }
				p.forwardMITM(clientConn, prepareRequestForRoundTrip(req2), flow)
				return
			}
		}
	}

	p.forwardMITM(clientConn, prepareRequestForRoundTrip(req), flow)
}

func (p *Proxy) forwardMITM(clientConn net.Conn, req *http.Request, flow *Flow) {
	resp, err := p.transport.RoundTrip(req)
	if err != nil {
		flow.Error = err.Error()
		flow.Pending = false
		flow.Duration = time.Since(flow.StartedAt)
		p.emit(flow)
		return
	}
	defer resp.Body.Close()

	flow.StatusCode = resp.StatusCode
	flow.ResponseHeader = cloneHeader(resp.Header)
	flow.ReqTruncated = false
	flow.RespTruncated = false

	respLB := NewLimitBuffer(p.cfg.MaxBodyBytes)
	resp.Body = readerCloser{Reader: io.TeeReader(resp.Body, respLB), Closer: resp.Body}

	bw := bufio.NewWriter(clientConn)
	_ = resp.Write(bw)
	_ = bw.Flush()

	flow.ResponseBody = respLB.Bytes()
	flow.RespTruncated = respLB.Truncated
	flow.Pending = false
	flow.Duration = time.Since(flow.StartedAt)
	p.emit(flow)
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
