package history

import (
	"bytes"
	"context"
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

func NewSink(address string) (*Sink, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("reference historian must be http://localhost:<port>")
	}
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
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
