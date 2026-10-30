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
	"regexp"
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
	steps := []struct {
		value float64
		at    time.Duration
		emit  bool
	}{
		{100, 0, true},                      // first value is always reported
		{101, 10 * time.Millisecond, false}, // noise within ±ε
		{103, 20 * time.Millisecond, false}, // still within ±ε of the reported 100
		{104, 30 * time.Millisecond, true},  // exceeds ε
		{104, 40 * time.Millisecond, false},
		{104, 80 * time.Millisecond, true}, // heartbeat after max silence
		{102.5, 90 * time.Millisecond, false},
	}
	for i, step := range steps {
		if got := filter.Apply(step.value, base.Add(step.at)); got != step.emit {
			t.Fatalf("step %d: Apply(%v)=%v, want %v", i, step.value, got, step.emit)
		}
	}
	filter.Reset()
	if !filter.Apply(104, base.Add(100*time.Millisecond)) {
		t.Fatal("expected the first value after Reset to be reported")
	}
}

func TestDeadbandSetOmitsSuppressedReadingsAndResetsOnBad(t *testing.T) {
	set := newDeadbandSet(map[string]DeadbandConfig{"temp": {Epsilon: 1, MaxSilenceInterval: time.Hour}})
	base := time.Now()
	poll := func(reading Reading) map[string]Reading {
		return set.filter(map[string]Reading{
			"temp":  reading,
			"other": {Value: 1.0, Quality: "good"},
		}, base)
	}
	if out := poll(Reading{Value: 20.0, Quality: "good", ObservedAt: base}); out["temp"].Value != 20.0 {
		t.Fatal(out)
	}
	out := poll(Reading{Value: 20.5, Quality: "good", ObservedAt: base.Add(time.Second)})
	if _, ok := out["temp"]; ok {
		t.Fatal("expected noise within ε to be omitted, not re-sent with a new timestamp", out)
	}
	if _, ok := out["other"]; !ok {
		t.Fatal("signals without a deadband must pass through", out)
	}
	if out := poll(Reading{Quality: "bad", Reason: "read-error"}); out["temp"].Quality != "bad" {
		t.Fatal("bad readings must never be suppressed", out)
	}
	if out := poll(Reading{Value: 20.5, Quality: "good", ObservedAt: base.Add(2 * time.Second)}); out["temp"].Value != 20.5 {
		t.Fatal("expected recovery after a bad reading to be reported", out)
	}
}

func TestDeadbandSetUsesPollClockAndReportsQualityChanges(t *testing.T) {
	set := newDeadbandSet(map[string]DeadbandConfig{
		"temp":     {Epsilon: 1, MaxSilenceInterval: time.Minute},
		"disabled": {Epsilon: 5},
	})
	stalled := time.Unix(1_800_000_000, 0) // device timestamp that never advances
	poll := func(value float64, quality string, now time.Time) map[string]Reading {
		return set.filter(map[string]Reading{
			"temp":     {Value: value, Quality: quality, ObservedAt: stalled},
			"disabled": {Value: value, Quality: "good"},
		}, now)
	}
	base := time.Now()
	poll(72, "good", base)
	out := poll(72, "uncertain", base.Add(time.Second))
	if out["temp"].Quality != "uncertain" {
		t.Fatal("a quality change must be reported even if the value is unchanged", out)
	}
	if _, ok := poll(72, "uncertain", base.Add(2*time.Second))["temp"]; ok {
		t.Fatal("expected the unchanged uncertain value to be suppressed")
	}
	if _, ok := poll(72, "uncertain", base.Add(2*time.Second+time.Minute))["temp"]; !ok {
		t.Fatal("expected a heartbeat by gateway poll time despite a stalled device timestamp")
	}
	if _, ok := out["disabled"]; !ok {
		t.Fatal("a zero MaxSilenceInterval must leave the signal unfiltered")
	}
}

func TestOPCUADeadbandReportsRecoveryAfterFailedPoll(t *testing.T) {
	base := time.Now().UTC()
	reader := &scriptedOPCUAReader{results: []scriptedOPCUAResult{
		{values: []ua.DataValue{{Value: 72.0, StatusCode: ua.Good, SourceTimestamp: base}}},
		{err: errors.New("connection reset")},
		{values: []ua.DataValue{{Value: 72.0, StatusCode: ua.Good, SourceTimestamp: base}}},
	}}
	driver := NewOPCUADriverWithSource("opc.tcp://127.0.0.1:4840", reader, nil, opcuapilot.Signals[:1], DriverOptions{
		ReadTimeout: 500 * time.Millisecond, RatePerSecond: 100, Burst: 4,
		Deadbands: map[string]DeadbandConfig{"motor_temperature": {Epsilon: 1, MaxSilenceInterval: time.Hour}},
	})
	name := opcuapilot.Signals[0].Name
	if out, _, _ := driver.Poll(context.Background()); out[name].Value != 72.0 {
		t.Fatal(out)
	}
	if out, _, reason := driver.Poll(context.Background()); reason != "read-error" || out[name].Quality != "bad" {
		t.Fatal(reason, out)
	}
	if out, _, _ := driver.Poll(context.Background()); out[name].Value != 72.0 || out[name].Quality != "good" {
		t.Fatal("expected recovery to be reported immediately after a failed poll", out)
	}
}

func TestCircuitBreakerAdmitsSingleHalfOpenProbe(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	guard := NewEndpointGuardrail("modbus-tcp://127.0.0.1:1502", 100, 4, 4)
	guard.breaker.now = func() time.Time { return now }
	guard.RecordFailure("read-timeout")
	guard.RecordFailure("read-timeout")
	now = now.Add(time.Second)
	if err := guard.Enter(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Enter(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal("expected only one probe while half-open", err)
	}
	guard.Leave()
	guard.RecordSuccess()
	if err := guard.Enter(); err != nil {
		t.Fatal(err)
	}
}

func TestPLC4GoDriverDoesNotReconnectAfterClose(t *testing.T) {
	connector := &sequencePLC4GoConnector{connections: []*fakePLC4GoConnection{{values: map[string]uint16{"temperature": 1}}}}
	driver, err := OpenPLC4GoDriver(context.Background(), connector, "modbus-tcp://127.0.0.1:1502", []PLC4GoTag{
		{Name: "temperature", Address: "holding-register:0"},
	}, DriverOptions{RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	driver.Close()
	if _, _, reason := driver.Poll(context.Background()); reason != "read-error" {
		t.Fatal(reason)
	}
	if connector.calls != 1 {
		t.Fatal("a closed driver must not open a new controller session", connector.calls)
	}
}

func TestCircuitBreakerBackoffGrowsAcrossFailedProbes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	breaker := NewCircuitBreaker(2, 100*time.Millisecond, 350*time.Millisecond)
	breaker.now = func() time.Time { return now }
	if breaker.RecordFailure() != ProducerStateDegraded || breaker.RecordFailure() != ProducerStateOffline {
		t.Fatal("expected degraded then offline")
	}
	for _, window := range []time.Duration{100, 200, 350, 350} {
		window *= time.Millisecond
		now = now.Add(window - time.Millisecond)
		if breaker.BeforeRequest() {
			t.Fatalf("expected requests to be refused inside the %v backoff window", window)
		}
		now = now.Add(time.Millisecond)
		if !breaker.BeforeRequest() {
			t.Fatalf("expected a half-open probe after %v", window)
		}
		if breaker.RecordFailure() != ProducerStateOffline {
			t.Fatal("a failed probe must reopen the breaker")
		}
	}
	if breaker.RecordSuccess() != ProducerStateGood {
		t.Fatal("expected success to close the breaker")
	}
	if breaker.RecordFailure() != ProducerStateDegraded {
		t.Fatal("expected the failure count to reset after success")
	}
}

func TestLocalScanProtectionDoesNotDegradeHealthyEndpoint(t *testing.T) {
	connection := &fakePLC4GoConnection{values: map[string]uint16{"temperature": 672}}
	var states []ProducerStatus
	driver, err := OpenPLC4GoDriver(context.Background(), &fakePLC4GoConnector{connection: connection}, "modbus-tcp://127.0.0.1:1502", []PLC4GoTag{
		{Name: "temperature", Address: "holding-register:0", Unit: "deciCel"},
	}, DriverOptions{ReadTimeout: 500 * time.Millisecond, RatePerSecond: 0.001, Burst: 1, StatusReporter: func(event ProducerStatusEvent) {
		states = append(states, event.State)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, reason := driver.Poll(context.Background()); reason != "" {
		t.Fatal(reason)
	}
	for range 3 {
		readings, _, reason := driver.Poll(context.Background())
		if reason != "scan-protection" || readings["temperature"].Quality != "bad" {
			t.Fatal(reason, readings)
		}
	}
	if len(states) != 0 {
		t.Fatal("rate-limited polls must not emit producer_status", states)
	}
	if !driver.guardrail.breaker.BeforeRequest() {
		t.Fatal("rate-limited polls must not open the circuit breaker")
	}
}

func TestPLC4GoDriverReconnectsAfterResetAndReportsBreakerStates(t *testing.T) {
	connector := &sequencePLC4GoConnector{connections: []*fakePLC4GoConnection{
		{err: errors.New("connection reset by peer")},
		{err: errors.New("connection reset by peer")},
		{values: map[string]uint16{"temperature": 680}},
	}}
	var states []ProducerStatus
	driver, err := OpenPLC4GoDriver(context.Background(), connector, "modbus-tcp://127.0.0.1:1502", []PLC4GoTag{
		{Name: "temperature", Address: "holding-register:0", Unit: "deciCel"},
	}, DriverOptions{ReadTimeout: 500 * time.Millisecond, RatePerSecond: 100, Burst: 4, StatusReporter: func(event ProducerStatusEvent) {
		states = append(states, event.State)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if _, _, reason := driver.Poll(context.Background()); reason != "read-error" {
		t.Fatal(reason)
	}
	if _, _, reason := driver.Poll(context.Background()); reason != "read-error" {
		t.Fatal(reason)
	}
	if _, _, reason := driver.Poll(context.Background()); reason != "circuit-open" {
		t.Fatal("expected the open breaker to skip the next poll", reason)
	}
	driver.guardrail.breaker.now = func() time.Time { return time.Now().UTC().Add(time.Second) }
	readings, _, reason := driver.Poll(context.Background())
	if reason != "" || readings["temperature"].Value != uint16(680) {
		t.Fatal(reason, readings)
	}
	if connector.calls != 3 {
		t.Fatal("expected a fresh connection after each reset", connector.calls)
	}
	for i, connection := range connector.connections[:2] {
		if connection.closeCalls != 1 {
			t.Fatalf("connection %d closed %d times, want 1", i, connection.closeCalls)
		}
	}
	want := []ProducerStatus{ProducerStateDegraded, ProducerStateOffline, ProducerStateGood}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatal(states)
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

// fakeTelegraf writes a wrapper that re-executes this test binary as a Telegraf
// 1.40.2 stand-in. mode selects the helper's behaviour in TestFakeTelegrafHelper.
func fakeTelegraf(t *testing.T, mode string) (binary, starts string) {
	t.Helper()
	tmp := t.TempDir()
	starts = filepath.Join(tmp, "starts.txt")
	binary = filepath.Join(tmp, "telegraf")
	wrapper := fmt.Sprintf("#!/bin/sh\nHADES_FAKE_TELEGRAF=%s HADES_TELEGRAF_STARTS=%s exec %s -test.run='^TestFakeTelegrafHelper$' -- \"$@\"\n", mode, starts, os.Args[0])
	if err := os.WriteFile(binary, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	return binary, starts
}

// TestFakeTelegrafHelper is the fake sidecar process, not a test. It reads the
// socket_writer address from the generated config and streams one JSON batch
// per 50ms, mimicking a long-lived Telegraf daemon.
func TestFakeTelegrafHelper(t *testing.T) {
	mode := os.Getenv("HADES_FAKE_TELEGRAF")
	if mode == "" {
		return
	}
	args := flagArgs()
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("Telegraf 1.40.2 (test)")
		os.Exit(0)
	}
	f, _ := os.OpenFile(os.Getenv("HADES_TELEGRAF_STARTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	fmt.Fprintln(f, "started")
	f.Close()
	if mode == "stall" {
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	config, err := os.ReadFile(args[1])
	if err != nil {
		os.Exit(2)
	}
	address := regexp.MustCompile(`address = "unix://([^"]+)"`).FindSubmatch(config)
	if address == nil {
		os.Exit(3)
	}
	conn, err := net.Dial("unix", string(address[1]))
	if err != nil {
		os.Exit(4)
	}
	for value := 672; ; value++ {
		if _, err := fmt.Fprintf(conn, `{"name":"hades_lab","fields":{"temperature":%d},"tags":{},"timestamp":1791979200000}`+"\n", value); err != nil {
			os.Exit(5)
		}
		if mode == "crash" && value == 673 {
			os.Exit(1)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func flagArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

func countStarts(t *testing.T, path string) int {
	t.Helper()
	raw, _ := os.ReadFile(path)
	return strings.Count(string(raw), "started")
}

func TestTailBufferKeepsRecentOutputWithoutFailing(t *testing.T) {
	buffer := &tailBuffer{limit: 8}
	for range 100 {
		if n, err := buffer.Write([]byte("0123456789")); err != nil || n != 10 {
			t.Fatal(n, err)
		}
	}
	if got := buffer.String(); got != "23456789" {
		t.Fatal(got)
	}
}

func TestModbusFactoryRekeysMeasurementDeadbandToRegister(t *testing.T) {
	cfg, err := sample.LoadContext("../..", "../../examples/site/modbus-lab.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var got DriverOptions
	factory := ProtocolDriverFactory{OpenTelegraf: func(_ context.Context, _, _ string, _ Profile, options DriverOptions) (ProtocolDriver, error) {
		got = options
		return &stubProtocolDriver{}, nil
	}}
	band := DeadbandConfig{Epsilon: 2, MaxSilenceInterval: time.Minute}
	if _, err := factory.Open(context.Background(), cfg, "tcp://127.0.0.1:1502", DriverOptions{Deadbands: map[string]DeadbandConfig{"motor_temperature": band}}, FactoryOptions{TelegrafBinary: "/tmp/telegraf"}); err != nil {
		t.Fatal(err)
	}
	if got.Deadbands["temperature"] != band {
		t.Fatal("expected the measurement deadband to apply to the lab register", got.Deadbands)
	}
}

func TestPersistentTelegrafSidecarReusesSingleChildAcrossPolls(t *testing.T) {
	binary, starts := fakeTelegraf(t, "stream")
	driver, err := OpenDriverWithOptions(context.Background(), binary, "tcp://127.0.0.1:1502", DefaultLabProfile(), DriverOptions{ReadTimeout: 2 * time.Second, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	config, err := os.ReadFile(driver.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "[[outputs.socket_writer]]") || !strings.Contains(string(config), "unix://"+driver.Socket) || strings.Contains(string(config), "--once") {
		t.Fatal(string(config))
	}
	info, err := os.Stat(filepath.Dir(driver.Socket))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("expected the sidecar socket in a private 0700 directory", info.Mode(), err)
	}
	last := uint16(0)
	for range 5 {
		values, _, reason := driver.ReadMap(context.Background())
		if reason != "" || values["temperature"] <= last {
			t.Fatal(values, reason)
		}
		last = values["temperature"]
	}
	if got := countStarts(t, starts); got != 1 || driver.SidecarStarts() != 1 {
		t.Fatalf("expected one long-lived sidecar across polls, started %d", got)
	}
	dir := filepath.Dir(driver.Socket)
	if err := driver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("expected Close to remove the private sidecar directory", err)
	}
}

func TestSupervisorRestartsCrashedTelegrafSidecar(t *testing.T) {
	binary, starts := fakeTelegraf(t, "crash")
	driver, err := OpenDriverWithOptions(context.Background(), binary, "tcp://127.0.0.1:1502", DefaultLabProfile(), DriverOptions{ReadTimeout: 2 * time.Second, RatePerSecond: 100, Burst: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	sawFailure := false
	deadline := time.Now().Add(10 * time.Second)
	for driver.SidecarStarts() < 2 && time.Now().Before(deadline) {
		if _, _, reason := driver.ReadMap(context.Background()); reason == "read-error" {
			sawFailure = true
		}
	}
	if driver.SidecarStarts() < 2 || countStarts(t, starts) < 2 {
		t.Fatal("expected the supervisor to restart the crashed sidecar", driver.SidecarStarts())
	}
	if !sawFailure {
		t.Fatal("expected the sidecar exit to surface as a read-error")
	}
	for time.Now().Before(deadline) {
		values, _, reason := driver.ReadMap(context.Background())
		if reason == "" && values["temperature"] >= 672 {
			return
		}
	}
	t.Fatal("expected readings to resume after the restart")
}

func TestDriverEmitsProducerStatusOnRepeatedFailures(t *testing.T) {
	script, _ := fakeTelegraf(t, "stall")
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

type scriptedOPCUAResult struct {
	values []ua.DataValue
	err    error
}

type scriptedOPCUAReader struct {
	results []scriptedOPCUAResult
	calls   int
}

func (r *scriptedOPCUAReader) Read(context.Context) ([]ua.DataValue, error) {
	result := r.results[min(r.calls, len(r.results)-1)]
	r.calls++
	return result.values, result.err
}

func (r *scriptedOPCUAReader) Close() {}

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
	if _, ok := second["motor_temperature"]; ok {
		t.Fatal("expected the deadband to suppress the 72.4 reading", second["motor_temperature"])
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

type fakeOPCUASubscription struct {
	results []scriptedOPCUAResult
	calls   int
	closed  int
}

func (s *fakeOPCUASubscription) Next(context.Context) ([]ua.DataValue, error) {
	result := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	return result.values, result.err
}

func (s *fakeOPCUASubscription) Close(context.Context) error {
	s.closed++
	return nil
}

func TestOPCUADriverRecreatesSubscriptionAfterSessionFailure(t *testing.T) {
	base := time.Now().UTC()
	good := []ua.DataValue{{Value: 70.0, StatusCode: ua.Good, SourceTimestamp: base}}
	subscriptions := []*fakeOPCUASubscription{
		{results: []scriptedOPCUAResult{{values: good}, {err: errors.New("BadSessionIdInvalid")}}},
		{results: []scriptedOPCUAResult{{values: good}}},
	}
	opened, created := 0, 0
	driver := NewOPCUADriverWithSource("opc.tcp://127.0.0.1:4840", nil, nil, opcuapilot.Signals[:1], DriverOptions{ReadTimeout: 500 * time.Millisecond, RatePerSecond: 100, Burst: 4})
	driver.opener = func(context.Context) (opcuaBatchReader, error) {
		opened++
		return &fakeOPCUAReader{batches: [][]ua.DataValue{good}}, nil
	}
	driver.subscribe = func(context.Context, opcuaBatchReader) (closableSubscription, error) {
		subscription := subscriptions[created]
		created++
		return subscription, nil
	}
	for i, want := range []string{"", "read-error", ""} {
		if _, _, reason := driver.Poll(context.Background()); reason != want {
			t.Fatalf("poll %d: reason=%q, want %q", i, reason, want)
		}
	}
	if opened != 2 || created != 2 {
		t.Fatalf("expected a new session and subscription after failure, opened=%d created=%d", opened, created)
	}
	if subscriptions[0].calls != 2 {
		t.Fatal("expected the first subscription to serve two polls without re-reading", subscriptions[0].calls)
	}
	driver.Close()
	if subscriptions[1].closed != 1 {
		t.Fatal("expected Close to delete the active subscription")
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
	err        error
	batches    [][]string
	closeCalls int
}

type sequencePLC4GoConnector struct {
	connections []*fakePLC4GoConnection
	calls       int
}

func (c *sequencePLC4GoConnector) Connect(context.Context, string) (PLC4GoConnection, error) {
	connection := c.connections[min(c.calls, len(c.connections)-1)]
	c.calls++
	return connection, nil
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
	return &fakePLC4GoRequest{values: b.connection.values, err: b.connection.err}, nil
}

type fakePLC4GoRequest struct {
	values map[string]uint16
	err    error
}

func (r *fakePLC4GoRequest) Execute(context.Context) (map[string]any, error) {
	if r.err != nil {
		return nil, r.err
	}
	values := make(map[string]any, len(r.values))
	for name, value := range r.values {
		values[name] = value
	}
	return values, nil
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
			copy(response[0:2], buf[0:2])
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
	if got := values["temperature"]; got != uint16(0) {
		t.Fatalf("temperature=%d, want 0", got)
	}
	if got := values["pressure"]; got != uint16(1) {
		t.Fatalf("pressure=%d, want 1", got)
	}
	if got := values["state"]; got != uint16(5) {
		t.Fatalf("state=%d, want 5", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestModbusResponseValidationRejectsMismatchedFrames(t *testing.T) {
	frame := func(transactionID uint16, unitID byte, pdu ...byte) []byte {
		out := make([]byte, 7, 7+len(pdu))
		binary.BigEndian.PutUint16(out[0:2], transactionID)
		binary.BigEndian.PutUint16(out[4:6], uint16(1+len(pdu)))
		out[6] = unitID
		return append(out, pdu...)
	}
	cases := []struct {
		name  string
		frame []byte
		want  string
	}{
		{"stale transaction", frame(6, 1, 0x03, 4, 0, 1, 0, 2), "transaction id 6"},
		{"short register count", frame(7, 1, 0x03, 2, 0, 1), "byte count 2 for 2 registers"},
		{"wrong unit", frame(7, 9, 0x03, 4, 0, 1, 0, 2), "unit id 9"},
		{"exception", frame(7, 1, 0x83, 0x02), "exception code 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			go func() {
				server.Write(tc.frame)
				server.Close()
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := readModbusHoldingRegisterResponse(ctx, client, 7, 1, 2)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		server.Write(frame(7, 1, 0x03, 4, 0, 1, 0, 2))
		server.Close()
	}()
	values, err := readModbusHoldingRegisterResponse(context.Background(), client, 7, 1, 2)
	if err != nil || len(values) != 2 || values[0] != 1 || values[1] != 2 {
		t.Fatal(values, err)
	}
}

func TestPLC4GoFactoryRequiresExplicitTagAddresses(t *testing.T) {
	cfg := &sample.Config{Metadata: map[string]any{
		"acquisition": map[string]any{"protocol": "plc4go", "gateway_id": "gw"},
		"measurements": map[string]any{
			"pressure":    map[string]any{"type": "number", "unit": "bar"},
			"temperature": map[string]any{"type": "number", "unit": "Cel"},
		},
	}}
	var opened []PLC4GoTag
	factory := ProtocolDriverFactory{OpenPLC4Go: func(_ context.Context, _ PLC4GoConnector, _ string, tags []PLC4GoTag, _ DriverOptions) (ProtocolDriver, error) {
		opened = tags
		return &stubProtocolDriver{}, nil
	}}
	connector := &fakePLC4GoConnector{}
	_, err := factory.Open(context.Background(), cfg, "s7://127.0.0.1", DriverOptions{}, FactoryOptions{PLC4Go: connector, PLC4GoTags: map[string]string{"temperature": "%DB1:0:INT"}})
	if err == nil || !strings.Contains(err.Error(), `"pressure" has no configured tag address`) {
		t.Fatal(err)
	}
	_, err = factory.Open(context.Background(), cfg, "s7://127.0.0.1", DriverOptions{}, FactoryOptions{PLC4Go: connector, PLC4GoTags: map[string]string{"temperature": "%DB1:0:INT", "pressure": "%DB1:2:INT", "speed": "%DB1:4:INT"}})
	if err == nil || !strings.Contains(err.Error(), `unknown measurement "speed"`) {
		t.Fatal(err)
	}
	if _, err = factory.Open(context.Background(), cfg, "s7://127.0.0.1", DriverOptions{}, FactoryOptions{PLC4Go: connector, PLC4GoTags: map[string]string{"temperature": "%DB1:0:INT", "pressure": "%DB1:2:INT"}}); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 || opened[0].Address != "%DB1:2:INT" || opened[1].Address != "%DB1:0:INT" || opened[1].Unit != "Cel" {
		t.Fatal(opened)
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
		filter.Apply(float64(100+i), base)
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
