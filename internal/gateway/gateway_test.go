package gateway

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/awcullen/opcua/ua"
	"github.com/openump/Hades/internal/normalize"
	"github.com/openump/Hades/internal/opcuapilot"
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
		w.Write([]byte(`{"acceptance":"accepted_broker_ack"}`))
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
		w.Write([]byte(`{"acceptance":"accepted_broker_ack"}`))
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

func TestEnvelopeForReadingPreservesSignalTimestampAndExtensions(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/opcua-mock.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, time.October, 19, 12, 0, 0, 0, time.UTC)
	raw, err := EnvelopeForReading(cfg, "motor_temperature", Reading{
		Value:      72.5,
		Unit:       "Cel",
		Quality:    "good",
		ObservedAt: observed,
		Extensions: map[string]any{"opcua.timestamp_origin": "source", "opcua.node_id": "Temperature"},
	}, sample.UUID(), 7, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["observed_at"] != observed.Format(sample.Timestamp) {
		t.Fatal(payload["observed_at"])
	}
	source := payload["source"].(map[string]any)
	if source["protocol"] != "opc-ua" || source["clock_origin"] != "device" {
		t.Fatal(source)
	}
	ext := payload["extensions"].(map[string]any)
	if ext["opcua.node_id"] != "Temperature" {
		t.Fatal(ext)
	}
	signals := payload["signals"].(map[string]any)
	if signals["motor_temperature"].(map[string]any)["value"] != 72.5 {
		t.Fatal(signals)
	}
	engine, err := normalize.NewOPCUAMock(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Map(raw); err != nil {
		t.Fatal(err)
	}
}
func TestCoalesceAdjacentReadsAndGuardrails(t *testing.T) {
	regs := []RegisterMapping{
		{Name: "temp", Address: 0},
		{Name: "temp2", Address: 1},
		{Name: "speed", Address: 5},
		{Name: "speed2", Address: 6},
	}
	plans := CoalesceAdjacentReads(regs)
	if len(plans) != 2 {
		t.Fatal("expected two contiguous plans", len(plans))
	}
	if plans[0].StartAddress != 0 || plans[0].EndAddress != 1 || len(plans[0].Registers) != 2 {
		t.Fatal(plans[0])
	}
	if plans[1].StartAddress != 5 || plans[1].EndAddress != 6 || len(plans[1].Registers) != 2 {
		t.Fatal(plans[1])
	}

	guard := NewEndpointGuardrail("tcp://127.0.0.1:1502", 0.5, 1, 1)
	if err := guard.Enter(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Enter(); err == nil {
		t.Fatal("expected max-in-flight enforcement")
	}
	guard.Leave()
	if guard.breaker == nil || guard.breaker.RecordFailure() != ProducerStateDegraded {
		t.Fatal("expected the first failure to degrade the endpoint")
	}
	if guard.breaker == nil || guard.breaker.RecordFailure() != ProducerStateOffline {
		t.Fatal("expected the second failure to trip circuit breaker")
	}
	if guard.Enter() == nil {
		t.Fatal("expected backoff enforcement")
	}
}

func TestDeadbandFilterEmitsHeartbeatOnMaxSilence(t *testing.T) {
	filter := NewDeadbandFilter(3, 50*time.Millisecond)
	base := time.Now()
	value, ok := filter.Apply(100, base)
	if !ok || value != 100 {
		t.Fatal(value, ok)
	}
	value, ok = filter.Apply(101, base.Add(10*time.Millisecond))
	if ok || value != 100 {
		t.Fatal(value, ok)
	}
	value, ok = filter.Apply(102, base.Add(20*time.Millisecond))
	if ok || value != 100 {
		t.Fatal(value, ok)
	}
	value, ok = filter.Apply(102, base.Add(200*time.Millisecond))
	if !ok || value != 102 {
		t.Fatal(value, ok)
	}
}

func TestPersistentSessionTracksReuseAcrossPollCycles(t *testing.T) {
	session := NewPersistentSession("tcp://127.0.0.1:1502")
	plan := []ReadRequestPlan{{StartAddress: 0, EndAddress: 1, Registers: []RegisterMapping{{Name: "temp", Address: 0}, {Name: "temp2", Address: 1}}}}
	session.RecordPoll(plan, "")
	if session.PollCount != 1 || len(session.ReadPlan) != 1 {
		t.Fatal(session)
	}
	session.Reset()
	if session.PollCount != 0 || session.LastError != "" {
		t.Fatal(session)
	}
}

func TestPersistentTelegrafSidecarReusesSingleChildAcrossPolls(t *testing.T) {
	tmp := t.TempDir()
	countFile := filepath.Join(tmp, "starts.txt")
	script := filepath.Join(tmp, "fake-telegraf.sh")
	body := strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = \"--version\" ]; then",
		"  echo 'Telegraf 1.40.2 (test)'",
		"  exit 0",
		"fi",
		"echo started >> \"$HADES_TELEGRAF_STARTS\"",
		"value=672",
		"while true; do",
		"  printf '{\"name\":\"hades_lab\",\"fields\":{\"temperature\":%s},\"tags\":{},\"timestamp\":1791979200000}\n' \"$value\"",
		"  value=$((value + 1))",
		"  sleep 0.05",
		"done",
	}, "\n")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HADES_TELEGRAF_STARTS", countFile)
	driver, err := OpenDriverWithOptions(context.Background(), script, "tcp://127.0.0.1:1502", DefaultLabProfile(), DriverOptions{ReadTimeout: 500 * time.Millisecond, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	values, _, reason := driver.ReadMap(context.Background())
	if reason != "" || values["temperature"] != 672 {
		t.Fatal(values, reason)
	}
	values, _, reason = driver.ReadMap(context.Background())
	if reason != "" || values["temperature"] != 673 {
		t.Fatal(values, reason)
	}
	starts, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(starts)), "started") != 1 {
		t.Fatal(string(starts))
	}
}

func TestDriverEmitsProducerStatusOnRepeatedFailures(t *testing.T) {
	tmp := t.TempDir()
	script := filepath.Join(tmp, "fake-telegraf-timeout.sh")
	body := strings.Join([]string{
		"#!/bin/sh",
		"if [ \"$1\" = \"--version\" ]; then",
		"  echo 'Telegraf 1.40.2 (test)'",
		"  exit 0",
		"fi",
		"sleep 5",
	}, "\n")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	states := []ProducerStatus{}
	driver, err := OpenDriverWithOptions(context.Background(), script, "tcp://127.0.0.1:1502", DefaultLabProfile(), DriverOptions{
		ReadTimeout:   100 * time.Millisecond,
		RatePerSecond: 100,
		Burst:         4,
		GatewayID:     "lab-modbus-01",
		StatusReporter: func(event ProducerStatusEvent) {
			states = append(states, event.State)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	for range 2 {
		values, _, reason := driver.ReadMap(context.Background())
		if values != nil || reason != "read-timeout" {
			t.Fatal(values, reason)
		}
	}
	if len(states) != 2 || states[0] != ProducerStateDegraded || states[1] != ProducerStateOffline {
		t.Fatal(states)
	}
}

type fakeOPCUAReader struct {
	batches [][]ua.DataValue
	calls   int
}

func (r *fakeOPCUAReader) Read(context.Context) ([]ua.DataValue, error) {
	if r.calls >= len(r.batches) {
		return r.batches[len(r.batches)-1], nil
	}
	result := r.batches[r.calls]
	r.calls++
	return result, nil
}

func (r *fakeOPCUAReader) Close() {}

type blockingOPCUAReader struct {
	unblock chan struct{}
	closed  bool
}

func (r *blockingOPCUAReader) Read(context.Context) ([]ua.DataValue, error) {
	<-r.unblock
	return nil, errors.New("closed")
}

func (r *blockingOPCUAReader) Close() {
	if r.closed {
		return
	}
	r.closed = true
	close(r.unblock)
}

func TestOPCUADriverMapsSignalsAndReusesPersistentSession(t *testing.T) {
	base := time.Now().UTC()
	reader := &fakeOPCUAReader{batches: [][]ua.DataValue{
		{
			{Value: 72.0, StatusCode: ua.Good, SourceTimestamp: base},
			{Value: uint32(10), StatusCode: ua.Good, SourceTimestamp: base.Add(10 * time.Millisecond)},
		},
		{
			{Value: 72.4, StatusCode: ua.Good, SourceTimestamp: base.Add(time.Second)},
			{Value: uint32(11), StatusCode: ua.Good, SourceTimestamp: base.Add(time.Second + 10*time.Millisecond)},
		},
	}}
	driver := NewOPCUADriverWithSource("opc.tcp://127.0.0.1:4840", reader, nil, opcuapilot.Signals[:2], DriverOptions{
		ReadTimeout:   500 * time.Millisecond,
		RatePerSecond: 100,
		Burst:         4,
		Deadbands: map[string]DeadbandConfig{
			"motor_temperature": {Epsilon: 1, MaxSilenceInterval: time.Hour},
		},
	})
	first, stamp, reason := driver.Poll(context.Background())
	if reason != "" {
		t.Fatal(reason)
	}
	if got := first["motor_temperature"].Value; got != 72.0 {
		t.Fatal(got)
	}
	if got := first["cycle_counter"].Value; got != uint32(10) {
		t.Fatal(got)
	}
	if !stamp.Equal(base.Add(10 * time.Millisecond)) {
		t.Fatal(stamp)
	}
	second, _, reason := driver.Poll(context.Background())
	if reason != "" {
		t.Fatal(reason)
	}
	if got := second["motor_temperature"].Value; got != 72.0 {
		t.Fatal(got)
	}
	if got := second["cycle_counter"].Value; got != uint32(11) {
		t.Fatal(got)
	}
	if reader.calls != 2 {
		t.Fatal(reader.calls)
	}
	if driver.session.PollCount != 2 {
		t.Fatal(driver.session.PollCount)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOPCUADriverReadTimeoutClosesBlockedReader(t *testing.T) {
	reader := &blockingOPCUAReader{unblock: make(chan struct{})}
	driver := NewOPCUADriverWithSource("opc.tcp://127.0.0.1:4840", reader, nil, opcuapilot.Signals[:1], DriverOptions{
		ReadTimeout:   50 * time.Millisecond,
		RatePerSecond: 100,
		Burst:         4,
	})
	start := time.Now()
	values, _, reason := driver.Poll(context.Background())
	if reason != "read-timeout" {
		t.Fatal(reason)
	}
	if values[opcuapilot.Signals[0].Name].Quality != "bad" {
		t.Fatal(values)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatal(elapsed)
	}
	if !reader.closed {
		t.Fatal("expected reader to be closed on timeout")
	}
}

type fakePLC4GoConnector struct {
	connection *fakePLC4GoConnection
	calls      int
}

func (c *fakePLC4GoConnector) Connect(context.Context, string) (PLC4GoConnection, error) {
	c.calls++
	return c.connection, nil
}

type fakePLC4GoConnection struct {
	values     map[string]uint16
	batches    [][]string
	closeCalls int
}

func (c *fakePLC4GoConnection) ReadRequestBuilder() PLC4GoReadRequestBuilder {
	return &fakePLC4GoBuilder{connection: c}
}

func (c *fakePLC4GoConnection) Close() error {
	c.closeCalls++
	return nil
}

type fakePLC4GoBuilder struct {
	connection *fakePLC4GoConnection
	names      []string
	addresses  []string
}

func (b *fakePLC4GoBuilder) AddTagAddress(name, address string) error {
	b.names = append(b.names, name)
	b.addresses = append(b.addresses, address)
	return nil
}

func (b *fakePLC4GoBuilder) Build() (PLC4GoReadRequest, error) {
	b.connection.batches = append(b.connection.batches, append([]string(nil), b.names...))
	return &fakePLC4GoRequest{values: b.connection.values}, nil
}

type fakePLC4GoRequest struct {
	values map[string]uint16
}

func (r *fakePLC4GoRequest) Execute(context.Context) (map[string]uint16, error) {
	return r.values, nil
}

type stubProtocolDriver struct{}

func (d *stubProtocolDriver) Poll(context.Context) (map[string]Reading, time.Time, string) {
	return nil, time.Time{}, ""
}

func (d *stubProtocolDriver) Close() error {
	return nil
}

func TestPLC4GoDriverCoalescesContiguousTagsAndReusesConnection(t *testing.T) {
	connection := &fakePLC4GoConnection{values: map[string]uint16{
		"temperature": 672,
		"pressure":    145,
		"state":       2,
	}}
	connector := &fakePLC4GoConnector{connection: connection}
	driver, err := OpenPLC4GoDriver(context.Background(), connector, "modbus-tcp://127.0.0.1:1502", []PLC4GoTag{
		{Name: "temperature", Address: "holding-register:0", Unit: "deciCel"},
		{Name: "pressure", Address: "holding-register:1", Unit: "bar"},
		{Name: "state", Address: "holding-register:5", Unit: "1"},
	}, DriverOptions{ReadTimeout: 500 * time.Millisecond, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	readings, _, reason := driver.Poll(context.Background())
	if reason != "" {
		t.Fatal(reason)
	}
	if len(driver.batches) != 2 {
		t.Fatal(driver.batches)
	}
	if len(connection.batches) != 2 {
		t.Fatal(connection.batches)
	}
	if got := strings.Join(connection.batches[0], ","); got != "temperature,pressure" {
		t.Fatal(got)
	}
	if got := strings.Join(connection.batches[1], ","); got != "state" {
		t.Fatal(got)
	}
	if readings["temperature"].Value != uint16(672) || readings["pressure"].Value != uint16(145) || readings["state"].Value != uint16(2) {
		t.Fatal(readings)
	}
	if _, _, reason = driver.Poll(context.Background()); reason != "" {
		t.Fatal(reason)
	}
	if connector.calls != 1 {
		t.Fatal(connector.calls)
	}
	if driver.session.PollCount != 2 {
		t.Fatal(driver.session.PollCount)
	}
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}
	if connection.closeCalls != 1 {
		t.Fatal(connection.closeCalls)
	}
}

func TestProtocolDriverFactorySelectsTelegrafForModbusMetadata(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "modbus-tcp"

	var gotBinary, gotEndpoint string
	var gotProfile Profile
	var gotOptions DriverOptions
	factory := ProtocolDriverFactory{
		OpenTelegraf: func(_ context.Context, binary, endpoint string, profile Profile, options DriverOptions) (ProtocolDriver, error) {
			gotBinary = binary
			gotEndpoint = endpoint
			gotProfile = profile
			gotOptions = options
			return &stubProtocolDriver{}, nil
		},
	}

	driver, err := factory.Open(context.Background(), cfg, "tcp://127.0.0.1:1502", DriverOptions{}, FactoryOptions{TelegrafBinary: "/tmp/telegraf"})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if gotBinary != "/tmp/telegraf" || gotEndpoint != "tcp://127.0.0.1:1502" {
		t.Fatal(gotBinary, gotEndpoint)
	}
	if gotProfile.MetricName != DefaultLabProfile().MetricName {
		t.Fatal(gotProfile)
	}
	if gotOptions.PollInterval != cfg.Interval {
		t.Fatal(gotOptions.PollInterval)
	}
}

func TestProtocolDriverFactorySelectsOPCUAForApprovedMock(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/opcua-mock.metadata.json")
	if err != nil {
		t.Fatal(err)
	}

	var gotEndpoint, gotPKI string
	var gotOptions DriverOptions
	factory := ProtocolDriverFactory{
		OpenOPCUA: func(_ context.Context, endpoint, pki string, options DriverOptions) (ProtocolDriver, error) {
			gotEndpoint = endpoint
			gotPKI = pki
			gotOptions = options
			return &stubProtocolDriver{}, nil
		},
	}

	driver, err := factory.Open(context.Background(), cfg, "opc.tcp://127.0.0.1:4840", DriverOptions{ReadTimeout: time.Second}, FactoryOptions{OPCUAPKI: "/tmp/pki"})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if gotEndpoint != "opc.tcp://127.0.0.1:4840" || gotPKI != "/tmp/pki" {
		t.Fatal(gotEndpoint, gotPKI)
	}
	if gotOptions.PollInterval != cfg.Interval || gotOptions.ReadTimeout != time.Second {
		t.Fatal(gotOptions)
	}
}

func TestProtocolDriverFactoryRejectsUnsupportedProtocols(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "simulator"
	if _, err := DefaultProtocolDriverFactory().Open(context.Background(), cfg, "ignored", DriverOptions{}, FactoryOptions{}); err == nil || !strings.Contains(err.Error(), "does not use a gateway protocol driver") {
		t.Fatal(err)
	}
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "plc4go"
	if _, err := DefaultProtocolDriverFactory().Open(context.Background(), cfg, "modbus-tcp://127.0.0.1:1502", DriverOptions{}, FactoryOptions{}); err == nil || !strings.Contains(err.Error(), "plc4go connector is required") {
		t.Fatal(err)
	}
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "not-a-protocol"
	if _, err := DefaultProtocolDriverFactory().Open(context.Background(), cfg, "ignored", DriverOptions{}, FactoryOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported gateway protocol") {
		t.Fatal(err)
	}
}

func TestPLC4GoModbusConnectorReadsContiguousRegistersOverLoopback(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		defer func() { serverErr <- nil }()

		for {
			buf := make([]byte, 12)
			if _, err := io.ReadFull(conn, buf); err != nil {
				if err == io.EOF {
					serverErr <- nil
					return
				}
				serverErr <- err
				return
			}
			if buf[6] != 0x01 {
				serverErr <- fmt.Errorf("unexpected unit id %d", buf[6])
				return
			}
			if buf[7] != 0x03 {
				serverErr <- fmt.Errorf("unexpected function code %d", buf[7])
				return
			}
			startAddr := binary.BigEndian.Uint16(buf[8:10])
			count := binary.BigEndian.Uint16(buf[10:12])
			payload := make([]byte, 1+1+2*count)
			payload[0] = 0x03
			payload[1] = byte(2 * count)
			for i := uint16(0); i < count; i++ {
				value := uint16(startAddr + i)
				binary.BigEndian.PutUint16(payload[2+i*2:2+(i+1)*2], value)
			}
			response := make([]byte, 7+len(payload))
			response[0] = 0x00
			response[1] = 0x00
			response[2] = 0x00
			response[3] = 0x00
			binary.BigEndian.PutUint16(response[4:6], uint16(3+2*count))
			response[6] = 0x01
			copy(response[7:], payload)
			if _, err := conn.Write(response); err != nil {
				serverErr <- err
				return
			}
			if startAddr == 5 {
				serverErr <- nil
				return
			}
		}
	}()

	connector := &ModbusTCPPLC4GoConnector{dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := connector.Connect(ctx, fmt.Sprintf("modbus-tcp://%s?unit=1", listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	builder := conn.ReadRequestBuilder()
	for _, tag := range []struct{ name, addr string }{{"temperature", "holding-register:0"}, {"pressure", "holding-register:1"}, {"state", "holding-register:5"}} {
		if err := builder.AddTagAddress(tag.name, tag.addr); err != nil {
			t.Fatal(err)
		}
	}
	request, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	values, err := request.Execute(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := values["temperature"]; got != 0 {
		t.Fatalf("temperature=%d, want 0", got)
	}
	if got := values["pressure"]; got != 1 {
		t.Fatalf("pressure=%d, want 1", got)
	}
	if got := values["state"]; got != 5 {
		t.Fatalf("state=%d, want 5", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func BenchmarkDriverConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := DriverConfig("tcp://127.0.0.1:1502"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSubmit(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw := []byte(`{"message_id":"immutable","observed_at":"preserved"}`)
	for i := 0; i < b.N; i++ {
		if err := Submit(ctx, http.DefaultClient, "http://127.0.0.1:8080", raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeReading(b *testing.B) {
	valid := []byte(`{"name":"hades_lab","fields":{"temperature":672},"tags":{},"timestamp":1791979200000}`)
	for i := 0; i < b.N; i++ {
		if _, _, err := DecodeReading(valid); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEnvelope(b *testing.B) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		b.Fatal(err)
	}
	cfg.Metadata["acquisition"].(map[string]any)["protocol"] = "modbus-tcp"
	value := uint16(672)
	for i := 0; i < b.N; i++ {
		if _, err := Envelope(cfg, &value, "read-timeout", sample.UUID(), 4, time.Now().UTC()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCoalesceAdjacentReads(b *testing.B) {
	regs := []RegisterMapping{
		{Name: "temp", Address: 0},
		{Name: "temp2", Address: 1},
		{Name: "speed", Address: 5},
		{Name: "speed2", Address: 6},
	}
	for i := 0; i < b.N; i++ {
		_ = CoalesceAdjacentReads(regs)
	}
}

func BenchmarkDeadbandFilter(b *testing.B) {
	filter := NewDeadbandFilter(3, 50*time.Millisecond)
	base := time.Now()
	for i := 0; i < b.N; i++ {
		filter.Apply(100+uint16(i), base)
	}
}

func BenchmarkPersistentSession(b *testing.B) {
	session := NewPersistentSession("tcp://127.0.0.1:1502")
	plan := []ReadRequestPlan{{StartAddress: 0, EndAddress: 1, Registers: []RegisterMapping{{Name: "temp", Address: 0}, {Name: "temp2", Address: 1}}}}
	for i := 0; i < b.N; i++ {
		session.RecordPoll(plan, "")
		session.Reset()
	}
}

func BenchmarkOPCUADriver(b *testing.B) {
	base := time.Now().UTC()
	reader := &fakeOPCUAReader{batches: [][]ua.DataValue{
		{
			{Value: 72.0, StatusCode: ua.Good, SourceTimestamp: base},
			{Value: uint32(10), StatusCode: ua.Good, SourceTimestamp: base.Add(10 * time.Millisecond)},
		},
		{
			{Value: 72.4, StatusCode: ua.Good, SourceTimestamp: base.Add(time.Second)},
			{Value: uint32(11), StatusCode: ua.Good, SourceTimestamp: base.Add(time.Second + 10*time.Millisecond)},
		},
	}}
	driver := NewOPCUADriverWithSource("opc.tcp://127.0.0.1:4840", reader, nil, opcuapilot.Signals[:2], DriverOptions{
		ReadTimeout:   500 * time.Millisecond,
		RatePerSecond: 100,
		Burst:         4,
		Deadbands: map[string]DeadbandConfig{
			"motor_temperature": {Epsilon: 1, MaxSilenceInterval: time.Hour},
		},
	})
	for i := 0; i < b.N; i++ {
		driver.Poll(context.Background())
	}
}

func TestOPCUAFailedPollNormalizesWithoutInventingDeviceEvidence(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/opcua-mock.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := normalize.NewOPCUAMock(cfg)
	if err != nil {
		t.Fatal(err)
	}
	driver := NewOPCUADriverWithSource("opc.tcp://127.0.0.1:4840", nil, nil, opcuapilot.Signals[:], DriverOptions{})
	for name, reading := range driver.failureReadings("read-timeout") {
		raw, err := EnvelopeForReading(cfg, name, reading, sample.UUID(), 0, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := engine.Map(raw)
		if err != nil {
			t.Fatalf("failed poll rejected: %v; %s", err, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(normalized, &out); err != nil {
			t.Fatal(err)
		}
		ext := out["extensions"].(map[string]any)
		if ext["opcua.timestamp_origin"] != "gateway" || ext["opcua.status_code"] != nil || ext["opcua.source_timestamp"] != nil || ext["opcua.server_timestamp"] != nil {
			t.Fatal(ext)
		}
		if reading.Value != nil || reading.Quality != "bad" || reading.Reason != "read-timeout" {
			t.Fatal(reading)
		}
	}
}

func TestOPCUAOfflineStartupEmitsBadReadingsAndRejectsMissingTrust(t *testing.T) {
	pki := filepath.Join(t.TempDir(), "pki")
	if err := opcuapilot.InitPKI(pki); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "opc.tcp://" + listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	driver, err := OpenOPCUADriver(ctx, endpoint, pki, DriverOptions{ReadTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	readings, _, reason := driver.Poll(ctx)
	if reason == "" || len(readings) != len(opcuapilot.Signals) {
		t.Fatal(readings, reason)
	}
	for _, reading := range readings {
		if reading.Value != nil || reading.Quality != "bad" {
			t.Fatal(reading)
		}
	}
	if _, err := OpenOPCUADriver(ctx, endpoint, t.TempDir(), DriverOptions{}); err == nil {
		t.Fatal("missing trust accepted")
	}
}

func TestVolatileAcceptanceCannotReleaseGatewayOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		w.Write([]byte(`{"acceptance":"accepted_volatile"}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Submit(ctx, server.Client(), server.URL, []byte(`{"message_id":"immutable"}`)); err == nil {
		t.Fatal("volatile receipt retired gateway ownership")
	}
}
