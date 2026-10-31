package transfer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/openump/Hades/internal/contracts"
)

type Receipt struct {
	Profile    string    `json:"profile"`
	Enterprise string    `json:"enterprise"`
	ID         string    `json:"message_id"`
	Digest     string    `json:"payload_digest"`
	ReceiptID  string    `json:"receipt_id"`
	State      string    `json:"state"`
	Accepted   time.Time `json:"accepted_at"`
}

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func CheckReceipt(raw []byte, enterprise string, r Record) error {
	if len(raw) > 4096 {
		return ErrReceipt
	}
	p, err := contracts.Decode(raw)
	obj, ok := p.(map[string]any)
	if err != nil || !ok || len(obj) != 7 {
		return ErrReceipt
	}
	for _, key := range []string{"profile", "enterprise", "message_id", "payload_digest", "receipt_id", "state", "accepted_at"} {
		if _, exists := obj[key]; !exists {
			return ErrReceipt
		}
	}
	var receipt Receipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return ErrReceipt
	}
	if receipt.Profile != Profile || receipt.Enterprise != enterprise || receipt.ID != r.ID || receipt.Digest != r.Digest || receipt.State != "durable" || !uuid.MatchString(receipt.ReceiptID) || receipt.Accepted.IsZero() {
		return ErrReceipt
	}
	return nil
}

type RetryError struct {
	Status int
	After  time.Duration
}

func (e *RetryError) Error() string { return fmt.Sprintf("transfer HTTP %d: retry", e.Status) }

type Sink struct {
	client          *http.Client
	url, enterprise string
}

func NewSink(endpoint, enterprise string, cfg *tls.Config) (*Sink, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "/v1/ingest/observations" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("explicit HTTPS transfer endpoint required")
	}
	if cfg == nil || cfg.InsecureSkipVerify || len(cfg.Certificates) == 0 || cfg.RootCAs == nil {
		return nil, fmt.Errorf("verified mTLS credentials required")
	}
	verifiedTLS := cfg.Clone()
	verifiedTLS.MinVersion = max(tls.VersionTLS13, verifiedTLS.MinVersion)
	tr := &http.Transport{TLSClientConfig: verifiedTLS, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 8192}
	return &Sink{url: endpoint, enterprise: enterprise, client: &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *Sink) Close() { s.client.CloseIdleConnections() }
func (s *Sink) Send(ctx context.Context, r Record) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(r.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpenUMP-Profile", Profile)
	req.Header.Set("X-OpenUMP-Topic", r.Topic)
	req.Header.Set("X-OpenUMP-Digest", r.Digest)
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrAuthorization
	}
	if res.StatusCode == 400 || res.StatusCode == 409 || res.StatusCode == 413 || res.StatusCode == 422 {
		return &PermanentError{Status: res.StatusCode}
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && res.StatusCode != 200 && res.StatusCode != 201 {
		return ErrReceipt
	}
	if res.StatusCode != 200 && res.StatusCode != 201 {
		hint := time.Duration(0)
		if seconds, err := strconv.ParseUint(res.Header.Get("Retry-After"), 10, 32); err == nil {
			hint = time.Duration(min(seconds, 300)) * time.Second
		}
		return &RetryError{Status: res.StatusCode, After: hint}
	}
	if res.Header.Get("Content-Type") != "application/json" {
		return ErrReceipt
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil {
		return err
	}
	if len(raw) > 4096 {
		return ErrReceipt
	}
	return CheckReceipt(raw, s.enterprise, r)
}
