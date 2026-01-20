package repeater

import (
	"crypto/tls"
	"io"
	"net/http"
	"time"

	"burpui/internal/httpraw"
)

func SendRaw(raw string, timeout time.Duration) (string, string, error) {
	req, _, err := httpraw.ParseRequest(raw)
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
