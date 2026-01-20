package repeater

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func SendRaw(raw string, timeout time.Duration) (string, string, error) {
	req, err := parseRawRequest(raw)
	if err != nil {
		return "", "", err
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}

	client := &http.Client{Timeout: timeout, Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return resp.Status, string(body), nil
}

func parseRawRequest(raw string) (*http.Request, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimSpace(raw) + "\n\n"

	br := bufio.NewReader(strings.NewReader(raw))
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, err
	}

	bodyBytes, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(bodyBytes)), nil
	}
	req.RequestURI = ""

	if req.URL == nil {
		req.URL = &url.URL{Path: "/"}
	}
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Host == "" {
		req.URL.Host = req.Host
	}
	if req.URL.Host == "" {
		return nil, fmt.Errorf("Host ausente")
	}
	return req, nil
}
