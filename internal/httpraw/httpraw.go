package httpraw

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func ParseRequest(raw string) (*http.Request, []byte, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.TrimSpace(raw) + "\n\n"

	br := bufio.NewReader(strings.NewReader(raw))
	req, err := http.ReadRequest(br)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, fmt.Errorf("Host ausente")
	}

	return req, bodyBytes, nil
}
