package history

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Sink struct {
	url    string
	client *http.Client
}

func NewSink(address string) (*Sink, error) { return NewSinkTLS(address, nil) }
func NewSinkTLS(address string, tlsConfig *tls.Config) (*Sink, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid historian URL")
	}
	if tlsConfig != nil {
		if u.Scheme != "https" {
			return nil, fmt.Errorf("TLS credentials require HTTPS historian")
		}
	} else if u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("cleartext historian must be loopback")
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	return &Sink{address + "/api/v1/import/prometheus", &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *Sink) Write(ctx context.Context, lines []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(lines))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "text/plain")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode == 200 || response.StatusCode == 204 {
		return nil
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != 408 && response.StatusCode != 429 {
		return &PermanentError{Status: response.StatusCode}
	}
	return fmt.Errorf("historian temporary/unknown HTTP %d", response.StatusCode)
}
func (s *Sink) Close() { s.client.CloseIdleConnections() }
