package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
)

func TestReadOnlyConfigurationRejectsRemoteEndpoints(t *testing.T) {
	if _, err := DriverConfig("tcp://192.0.2.10:502"); err == nil {
		t.Fatal("remote endpoint allowed without approved pilot")
	}
	cfg, err := DriverConfig("tcp://127.0.0.1:1502")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "address = [0]") || !strings.Contains(cfg, "data_type = \"UINT16\"") || !strings.Contains(cfg, "scale = 1.0") || strings.Contains(cfg, "input_registers") {
		t.Fatal(cfg)
	}
}
func TestEnvelopeHasImmutableIdentityAndNoMissingZero(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "modbus-tcp"
	for _, failure := range []bool{false, true} {
		value := uint16(672)
		var input *uint16 = &value
		if failure {
			input = nil
		}
		raw, err := Envelope(cfg, input, "read-timeout", sample.UUID(), 4, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		json.Unmarshal(raw, &p)
		if p["source"].(map[string]any)["clock_uncertainty_ms"] != nil {
			t.Fatal("fabricated clock trust")
		}
		reading := p["signals"].(map[string]any)["temperature"].(map[string]any)
		if failure && (reading["value"] != nil || reading["quality"] != "bad") {
			t.Fatal(reading)
		}
		if !failure && reading["value"] != float64(672) {
			t.Fatal(reading)
		}
	}
}
func TestRejectAmbiguousDriverOutput(t *testing.T) {
	valid := []byte(`{"name":"hades_lab","fields":{"temperature":672},"tags":{},"timestamp":1791979200000}`)
	value, _, err := DecodeReading(valid)
	if err != nil || value != 672 {
		t.Fatal(value, err)
	}
	for _, raw := range [][]byte{[]byte(`{}`), append(append([]byte(nil), valid...), valid...), []byte(`{"name":"hades_lab","fields":{"temperature":0,"other":1},"timestamp":1791979200000}`)} {
		if _, _, err := DecodeReading(raw); err == nil {
			t.Fatal("ambiguous metric accepted")
		}
	}
}
func TestRetryPreservesTheOriginalObservation(t *testing.T) {
	bodies := [][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"acceptance":"accepted_volatile"}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw := []byte(`{"message_id":"immutable","observed_at":"preserved"}`)
	if err := Submit(ctx, server.Client(), server.URL, raw); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || !bytes.Equal(raw, bodies[0]) {
		t.Fatal("retry changed observation")
	}
}
func TestLostAcceptanceBodyRetainsAndRetries(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(202)
			w.Write([]byte(`{"acceptance":`))
			return
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"acceptance":"accepted_volatile"}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Submit(ctx, server.Client(), server.URL, []byte(`{"immutable":true}`)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestMultiRegisterProfileDriverConfigDecodeAndEnvelope(t *testing.T) {
	profile := Profile{
		MetricName: "hades_press",
		SlaveID:    2,
		Registers: []RegisterMapping{
			{Name: "temperature", Address: 0, DataType: "UINT16", ByteOrder: "AB", Scale: 1.0, Unit: "deciCel"},
			{Name: "hydraulic_pressure", Address: 1, DataType: "UINT16", ByteOrder: "AB", Scale: 1.0, Unit: "bar"},
		},
	}
	conf, err := DriverConfigForProfile("tcp://127.0.0.1:1502", profile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, `name = "hydraulic_pressure"`) || !strings.Contains(conf, `slave_id = 2`) {
		t.Fatal(conf)
	}
	raw := []byte(`{"name":"hades_press","fields":{"temperature":672,"hydraulic_pressure":145},"tags":{},"timestamp":1791979200000}`)
	values, _, err := DecodeReadingsForProfile(raw, profile)
	if err != nil || values["temperature"] != 672 || values["hydraulic_pressure"] != 145 {
		t.Fatal(values, err)
	}
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	envRaw, err := EnvelopeForProfile(cfg, profile, values, "", sample.UUID(), 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(envRaw, &p); err != nil {
		t.Fatal(err)
	}
	signals := p["signals"].(map[string]any)
	if len(signals) != 2 || signals["hydraulic_pressure"].(map[string]any)["unit"] != "bar" {
		t.Fatal(signals)
	}
}

