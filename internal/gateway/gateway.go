// Package gateway is a loopback-only lab wrapper around the selected Telegraf driver.
package gateway

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
)

func DriverConfig(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() || u.Scheme != "tcp" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("lab gateway requires literal loopback tcp://IP:port; real pilot not approved")
	}
	return fmt.Sprintf(`[agent]
  omit_hostname = true
  quiet = true
[[inputs.modbus]]
  name = "hades_lab"
  controller = %q
  slave_id = 1
  timeout = "200ms"
  busy_retries = 0
  configuration_type = "register"
  holding_registers = [
    { measurement = "hades_lab", name = "temperature", byte_order = "AB", data_type = "UINT16", scale = 1.0, address = [0] }
  ]
[[outputs.file]]
  files = ["stdout"]
  data_format = "json"
  json_timestamp_units = "1ms"
`, endpoint), nil
}
func DecodeReading(raw []byte) (uint16, time.Time, error) {
	value, err := contracts.Decode(raw)
	if err != nil {
		return 0, time.Time{}, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return 0, time.Time{}, fmt.Errorf("expected driver object")
	}
	if metrics, exists := obj["metrics"]; exists {
		items, ok := metrics.([]any)
		if !ok || len(items) != 1 || len(obj) != 1 {
			return 0, time.Time{}, fmt.Errorf("expected one driver metric")
		}
		obj, ok = items[0].(map[string]any)
		if !ok {
			return 0, time.Time{}, fmt.Errorf("invalid metric")
		}
	}
	fields, ok := obj["fields"].(map[string]any)
	if !ok || len(fields) != 1 || obj["name"] != "hades_lab" {
		return 0, time.Time{}, fmt.Errorf("unexpected driver signal/name")
	}
	number, ok := fields["temperature"].(json.Number)
	if !ok {
		return 0, time.Time{}, fmt.Errorf("missing numeric raw value")
	}
	n, err := number.Int64()
	if err != nil || n < 0 || n > 65535 {
		return 0, time.Time{}, fmt.Errorf("raw value outside UINT16")
	}
	stamp, ok := obj["timestamp"].(json.Number)
	if !ok {
		return 0, time.Time{}, fmt.Errorf("missing driver timestamp")
	}
	ms, err := stamp.Int64()
	if err != nil || ms < 0 || ms > 4102444800000 {
		return 0, time.Time{}, fmt.Errorf("invalid millisecond timestamp")
	}
	return uint16(n), time.UnixMilli(ms).UTC(), nil
}
func Envelope(cfg *sample.Config, value *uint16, reason, boot string, sequence int, observed time.Time) ([]byte, error) {
	var reading any
	quality := "good"
	if value != nil {
		reading = *value
	} else {
		quality = "bad"
	}
	signal := map[string]any{"value": reading, "unit": "deciCel", "quality": quality}
	if value == nil {
		signal["reason"] = reason
	}
	p := map[string]any{"schema_version": "1.0", "message_id": sample.UUID(), "asset_id": cfg.Metadata["asset_id"], "metadata_revision": cfg.Metadata["metadata_revision"], "observed_at": observed.UTC().Format(sample.Timestamp), "ingested_at": time.Now().UTC().Format(sample.Timestamp), "source": map[string]any{"gateway_id": cfg.Metadata["acquisition"].(map[string]any)["gateway_id"], "protocol": "modbus-tcp", "boot_id": boot, "sequence": sequence, "clock_origin": "gateway", "clock_uncertainty_ms": nil}, "signals": map[string]any{"temperature": signal}}
	return json.Marshal(p)
}
