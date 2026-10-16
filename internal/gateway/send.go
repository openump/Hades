package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/openump/Hades/internal/delivery"
)

// Submit owns one immutable volatile observation until explicit MQTT broker acknowledgement.
func Submit(ctx context.Context, client *http.Client, endpoint string, raw []byte) error {
	return SubmitObserved(ctx, client, endpoint, raw, func() {})
}
func SubmitObserved(ctx context.Context, client *http.Client, endpoint string, raw []byte, retry func()) error {
	for attempt := 0; ; attempt++ {
		hint := time.Duration(0)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err == nil {
			body, readerr := io.ReadAll(io.LimitReader(response.Body, 4097))
			response.Body.Close()
			if seconds, parseErr := strconv.Atoi(response.Header.Get("Retry-After")); parseErr == nil && seconds >= 0 {
				hint = time.Duration(min(seconds, 300)) * time.Second
			}
			if response.StatusCode == 202 && readerr == nil && len(body) <= 4096 {
				var receipt struct {
					Acceptance string `json:"acceptance"`
				}
				if json.Unmarshal(body, &receipt) == nil && receipt.Acceptance == "accepted_broker_ack" {
					return nil
				}
				// Ambiguous acceptance must not retire the original observation.
			}
			if response.StatusCode != 202 && response.StatusCode != 503 && response.StatusCode != 408 && response.StatusCode != 429 && response.StatusCode < 500 {
				return fmt.Errorf("permanent ingress rejection: HTTP %d", response.StatusCode)
			}
		}
		retry()
		timer := time.NewTimer(max(delivery.Delay(attempt), hint))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
