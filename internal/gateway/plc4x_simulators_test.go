//go:build plc4x

package gateway

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The Hades-040 simulator matrix runs only with HADES_PLC_SIMULATORS=1, a Python
// interpreter that has tests/fixtures/plc-simulators/requirements.txt installed
// (HADES_PLC_PYTHON, default python3) and the libplctag ab_server binary
// (HADES_AB_SERVER, built by scripts/build-ab-server). Set HADES_PLC_EVIDENCE to
// write a JSON evidence report.

func requireSimulators(t *testing.T) {
	t.Helper()
	if os.Getenv("HADES_PLC_SIMULATORS") != "1" {
		t.Skip("set HADES_PLC_SIMULATORS=1 to run the PLC simulator matrix")
	}
}

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// simulator is one running fixture process with its write/read ledger.
type simulator struct {
	t      *testing.T
	cmd    *exec.Cmd
	ledger string
	output *strings.Builder
}

func startSimulator(t *testing.T, ledger string, name string, args ...string) *simulator {
	t.Helper()
	cmd := exec.Command(name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	output := &strings.Builder{}
	cmd.Stderr = &lockedWriter{w: output}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	s := &simulator{t: t, cmd: cmd, ledger: ledger, output: output}
	t.Cleanup(s.stop)
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "ready") {
				ready <- nil
				io.Copy(io.Discard, stdout)
				return
			}
		}
		ready <- fmt.Errorf("simulator exited before ready")
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, output)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not become ready\n%s", name, output)
	}
	return s
}

func (s *simulator) stop() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait() // releases exec's output goroutines and pipes
	}
}

// ledgerCounts returns the read PDU count, read item count and write count the
// fixture recorded.
func (s *simulator) ledgerCounts() (reads, items, writes int) {
	raw, err := os.ReadFile(s.ledger)
	if err != nil {
		return 0, 0, 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var entry struct {
			Kind  string `json:"kind"`
			Items int    `json:"items"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		switch entry.Kind {
		case "read":
			reads++
			items += entry.Items
		case "write":
			writes++
		}
	}
	return reads, items, writes
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// tcpProxy forwards a loopback port to a simulator, counting accepted client
// connections (session reuse) and passing client bytes to an optional
// inspector (wire-level read/write classification).
type tcpProxy struct {
	listener net.Listener
	target   string
	accepts  atomic.Int64
	inspect  func([]byte)
	mu       sync.Mutex
	conns    []net.Conn
}

func startTCPProxy(t *testing.T, target string, inspect func([]byte)) *tcpProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{listener: listener, target: target, inspect: inspect}
	t.Cleanup(func() { listener.Close(); p.dropAll() })
	go p.serve()
	return p
}

func (p *tcpProxy) port() int { return p.listener.Addr().(*net.TCPAddr).Port }

func (p *tcpProxy) serve() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.accepts.Add(1)
		server, err := net.Dial("tcp", p.target)
		if err != nil {
			client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, server)
		p.mu.Unlock()
		go func() {
			defer server.Close()
			reader := io.Reader(client)
			if p.inspect != nil {
				reader = io.TeeReader(client, inspectWriter(p.inspect))
			}
			io.Copy(server, reader)
		}()
		go func() {
			defer client.Close()
			io.Copy(client, server)
		}()
	}
}

// dropAll resets every proxied session, simulating a controller reset.
func (p *tcpProxy) dropAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		conn.Close()
	}
	p.conns = nil
}

type inspectWriter func([]byte)

func (f inspectWriter) Write(b []byte) (int, error) {
	f(append([]byte(nil), b...))
	return len(b), nil
}

// cipServiceCounter classifies EtherNet/IP encapsulation frames on the wire.
// It counts CIP Read Tag services as reads and every Write Tag / Read-Modify-
// Write service as a write, including those nested in Multiple Service Packets.
type cipServiceCounter struct {
	mu     sync.Mutex
	buffer []byte
	reads  int
	items  int
	writes int
}

const (
	cipReadTag          = 0x4C
	cipReadTagFragment  = 0x52
	cipWriteTag         = 0x4D
	cipWriteTagFragment = 0x53
	cipReadModifyWrite  = 0x4E
	cipMultipleService  = 0x0A
	cipUnconnectedSend  = 0x52
	eipSendRRData       = 0x6F
	eipSendUnitData     = 0x70
)

func (c *cipServiceCounter) feed(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buffer = append(c.buffer, b...)
	for len(c.buffer) >= 24 {
		length := int(binary.LittleEndian.Uint16(c.buffer[2:4]))
		if len(c.buffer) < 24+length {
			return
		}
		command := binary.LittleEndian.Uint16(c.buffer[0:2])
		if command == eipSendRRData || command == eipSendUnitData {
			c.classifyCPF(c.buffer[24 : 24+length])
		}
		c.buffer = c.buffer[24+length:]
	}
}

// classifyCPF walks the common packet format items to the CIP request.
func (c *cipServiceCounter) classifyCPF(data []byte) {
	if len(data) < 8 {
		return
	}
	count := int(binary.LittleEndian.Uint16(data[6:8]))
	offset := 8
	for i := 0; i < count && offset+4 <= len(data); i++ {
		itemType := binary.LittleEndian.Uint16(data[offset : offset+2])
		itemLength := int(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
		payload := data[offset+4 : min(offset+4+itemLength, len(data))]
		switch itemType {
		case 0xB2: // unconnected data
			c.classifyCIP(payload, 0)
		case 0xB1: // connected data, prefixed by a sequence count
			if len(payload) > 2 {
				c.classifyCIP(payload[2:], 0)
			}
		}
		offset += 4 + itemLength
	}
}

// classifyCIP counts one CIP request. 0x52 is both Read Tag Fragment and
// Unconnected Send, so the request path decides: only a request addressed to the
// Connection Manager (class 0x06, instance 1) is an Unconnected Send, whose
// embedded request is classified recursively. Multiple Service Packet members
// are classified recursively too, so a nested write is never hidden.
func (c *cipServiceCounter) classifyCIP(request []byte, depth int) {
	if len(request) < 2 || depth > 4 {
		return
	}
	service := request[0] & 0x7F
	pathWords := int(request[1])
	if 2+2*pathWords > len(request) {
		return
	}
	path := request[2 : 2+2*pathWords]
	body := request[2+2*pathWords:]
	switch {
	case service == cipUnconnectedSend && isConnectionManagerPath(path):
		// Priority/tick and timeout bytes, then the embedded request length.
		if len(body) < 4 {
			return
		}
		inner := int(binary.LittleEndian.Uint16(body[2:4]))
		c.classifyCIP(body[4:min(4+inner, len(body))], depth+1)
	case service == cipMultipleService:
		c.reads++
		if len(body) < 2 {
			return
		}
		n := int(binary.LittleEndian.Uint16(body[0:2]))
		for i := 0; i < n && 2+2*i+2 <= len(body); i++ {
			start := int(binary.LittleEndian.Uint16(body[2+2*i:]))
			end := len(body)
			if i+1 < n && 2+2*(i+1)+2 <= len(body) {
				end = int(binary.LittleEndian.Uint16(body[2+2*(i+1):]))
			}
			if start < end && end <= len(body) {
				c.classifyMember(body[start:end], depth+1)
			}
		}
	case service == cipReadTag || service == cipReadTagFragment:
		c.reads++
		c.items++
	case service == cipWriteTag || service == cipWriteTagFragment || service == cipReadModifyWrite:
		c.writes++
	}
}

// classifyMember counts one Multiple Service Packet member as an item rather
// than a separate request.
func (c *cipServiceCounter) classifyMember(request []byte, depth int) {
	reads := c.reads
	c.classifyCIP(request, depth)
	if c.reads > reads {
		c.reads = reads // the packet itself already counted as one request
	}
}

// isConnectionManagerPath reports whether a CIP path is the logical segment
// path to the Connection Manager: class 0x06, instance 0x01.
func isConnectionManagerPath(path []byte) bool {
	return len(path) == 4 && path[0] == 0x20 && path[1] == 0x06 && path[2] == 0x24 && path[3] == 0x01
}

func (c *cipServiceCounter) counts() (reads, items, writes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads, c.items, c.writes
}

// protocolEvidence is one row of the Hades-040 evidence report.
type protocolEvidence struct {
	Protocol        string         `json:"protocol"`
	Simulator       string         `json:"simulator"`
	Endpoint        string         `json:"endpoint"`
	Values          map[string]any `json:"values"`
	Polls           int            `json:"polls"`
	Tags            int            `json:"tags"`
	ReadRequests    int            `json:"read_requests"`
	ReadItems       int            `json:"read_items"`
	TCPSessions     int64          `json:"tcp_sessions,omitempty"`
	Reconnected     bool           `json:"reconnected_after_reset"`
	WriteServices   int            `json:"write_services"`
	ElapsedMS       float64        `json:"elapsed_ms_per_poll"`
	RequestsPerPoll float64        `json:"read_requests_per_poll"`
}

var (
	evidenceMu sync.Mutex
	evidence   []protocolEvidence
)

func recordEvidence(t *testing.T, row protocolEvidence) {
	t.Helper()
	row.RequestsPerPoll = float64(row.ReadRequests) / float64(max(row.Polls, 1))
	evidenceMu.Lock()
	evidence = append(evidence, row)
	rows := append([]protocolEvidence(nil), evidence...)
	evidenceMu.Unlock()
	if path := os.Getenv("HADES_PLC_EVIDENCE"); path != "" {
		raw, err := json.MarshalIndent(map[string]any{"protocols": rows}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "tests", "fixtures", "plc-simulators", name)
}

func pythonFixture(t *testing.T, script string, port int) (*simulator, string) {
	t.Helper()
	python := os.Getenv("HADES_PLC_PYTHON")
	if python == "" {
		python = "python3"
	}
	ledger := filepath.Join(t.TempDir(), "ledger.jsonl")
	return startSimulator(t, ledger, python, fixturePath(script), strconv.Itoa(port), ledger), ledger
}

// matrixCase describes one protocol run through the gateway's PLC4Go driver.
type matrixCase struct {
	protocol string
	tags     []PLC4GoTag
	want     map[string]any
}

// pollMatrix runs polls through the full PLC4Go driver (guardrails included)
// and checks every reading is good and equal to the expected typed value.
func pollMatrix(t *testing.T, endpoint string, mc matrixCase, polls int) (map[string]any, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	driver, err := OpenPLC4GoDriver(ctx, NewPLC4XConnector(), endpoint, mc.tags, DriverOptions{ReadTimeout: 10 * time.Second, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatalf("%s: open: %v", mc.protocol, err)
	}
	defer driver.Close()
	values := map[string]any{}
	started := time.Now()
	for poll := 0; poll < polls; poll++ {
		readings, _, reason := driver.Poll(ctx)
		if reason != "" {
			t.Fatalf("%s poll %d: %s", mc.protocol, poll, reason)
		}
		for name, want := range mc.want {
			reading := readings[name]
			if reading.Quality != "good" || !sameNumber(reading.Value, want) {
				t.Fatalf("%s poll %d: %s=%#v (%s %s), want %#v", mc.protocol, poll, name, reading.Value, reading.Quality, reading.Reason, want)
			}
			values[name] = reading.Value
		}
	}
	return values, time.Since(started) / time.Duration(polls)
}

func sameNumber(got, want any) bool {
	g, ok1 := asFloat64(got)
	w, ok2 := asFloat64(want)
	if !ok1 || !ok2 {
		return false
	}
	return math.Abs(g-w) < 1e-4
}

// reconnectAfterReset drops every proxied session and checks the driver
// reports the failure, then recovers on a fresh session.
func reconnectAfterReset(t *testing.T, proxy *tcpProxy, endpoint string, mc matrixCase) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var states []ProducerStatus
	driver, err := OpenPLC4GoDriver(ctx, NewPLC4XConnector(), endpoint, mc.tags, DriverOptions{ReadTimeout: 5 * time.Second, RatePerSecond: 100, Burst: 8, StatusReporter: func(event ProducerStatusEvent) {
		states = append(states, event.State)
	}})
	if err != nil {
		t.Fatalf("%s: open: %v", mc.protocol, err)
	}
	defer driver.Close()
	if _, _, reason := driver.Poll(ctx); reason != "" {
		t.Fatalf("%s: first poll: %s", mc.protocol, reason)
	}
	before := proxy.accepts.Load()
	proxy.dropAll()
	sawFailure := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		readings, _, reason := driver.Poll(ctx)
		if reason != "" {
			sawFailure = true
			driver.guardrail.breaker.now = func() time.Time { return time.Now().UTC().Add(time.Minute) }
			continue
		}
		for name, want := range mc.want {
			if !sameNumber(readings[name].Value, want) {
				t.Fatalf("%s after reset: %s=%#v, want %#v", mc.protocol, name, readings[name].Value, want)
			}
		}
		if proxy.accepts.Load() <= before {
			t.Fatalf("%s: recovered without opening a new session", mc.protocol)
		}
		if sawFailure && (len(states) == 0 || states[len(states)-1] != ProducerStateGood) {
			t.Fatalf("%s: expected producer_status to return to good, got %v", mc.protocol, states)
		}
		return true
	}
	t.Fatalf("%s: did not recover after a session reset", mc.protocol)
	return false
}

const matrixPolls = 5

func TestSimulatorS7(t *testing.T) {
	requireSimulators(t)
	port := freePort(t, "tcp")
	sim, _ := pythonFixture(t, "s7_server.py", port)
	proxy := startTCPProxy(t, fmt.Sprintf("127.0.0.1:%d", port), nil)
	endpoint := fmt.Sprintf("s7://127.0.0.1:%d?cotp.remote-rack=0&cotp.remote-slot=1", proxy.port())
	mc := matrixCase{protocol: "s7", tags: []PLC4GoTag{
		{Name: "real", Address: "%DB1:0:REAL"}, {Name: "dint", Address: "%DB1:4:DINT"},
		{Name: "int", Address: "%DB1:8:INT"}, {Name: "uint", Address: "%DB1:10:UINT"},
		{Name: "bool", Address: "%DB1:12.0:BOOL"},
	}, want: map[string]any{"real": 3.25, "dint": -123456, "int": -42, "uint": 4242, "bool": 1}}
	values, elapsed := pollMatrix(t, endpoint, mc, matrixPolls)
	reads, items, writes := sim.ledgerCounts()
	sessions := proxy.accepts.Load()
	if writes != 0 {
		t.Fatalf("s7: %d write services reached the controller", writes)
	}
	if reads != matrixPolls || items != matrixPolls*len(mc.tags) {
		t.Fatalf("s7: expected one multi-item read PDU per poll (%d PDUs, %d items), got %d PDUs, %d items", matrixPolls, matrixPolls*len(mc.tags), reads, items)
	}
	if sessions != 1 {
		t.Fatalf("s7: expected one persistent session across polls, got %d", sessions)
	}
	reconnected := reconnectAfterReset(t, proxy, endpoint, mc)
	if _, _, writes := sim.ledgerCounts(); writes != 0 {
		t.Fatalf("s7: %d write services after reconnect", writes)
	}
	recordEvidence(t, protocolEvidence{Protocol: "s7", Simulator: "python-snap7 3.2.1", Endpoint: endpoint, Values: values, Polls: matrixPolls, Tags: len(mc.tags), ReadRequests: reads, ReadItems: items, TCPSessions: sessions, Reconnected: reconnected, ElapsedMS: float64(elapsed.Microseconds()) / 1000})
}

func TestSimulatorADS(t *testing.T) {
	requireSimulators(t)
	if raceDetectorEnabled {
		// Known plc4go defect at the pinned commit: ads.(*Connection).subscribe
		// writes its subscriptions map from per-tag goroutines without a lock
		// during every connect handshake (internal/ads/Subscriber.go:187). The
		// race detector reports it on each ADS connect; CI runs this test
		// without -race. Production ADS use is blocked until upstream fixes it.
		t.Skip("plc4go ADS connect has a known unsynchronized map write; run without -race")
	}
	port := freePort(t, "tcp")
	sim, _ := pythonFixture(t, "ads_server.py", port)
	proxy := startTCPProxy(t, fmt.Sprintf("127.0.0.1:%d", port), nil)
	endpoint := fmt.Sprintf("ads://127.0.0.1:%d?target-ams-net-id=127.0.0.1.1.1&target-ams-port=851&source-ams-net-id=127.0.0.1.1.2&source-ams-port=33000", proxy.port())
	mc := matrixCase{protocol: "ads", tags: []PLC4GoTag{
		{Name: "real", Address: "GVL.fReal"}, {Name: "dint", Address: "GVL.nDint"},
		{Name: "int", Address: "GVL.nInt"}, {Name: "uint", Address: "GVL.nUint"},
		{Name: "bool", Address: "GVL.bFlag"}, {Name: "direct_real", Address: "0x4020/0:REAL"},
	}, want: map[string]any{"real": 3.25, "dint": -123456, "int": -42, "uint": 4242, "bool": 1, "direct_real": 3.25}}
	values, elapsed := pollMatrix(t, endpoint, mc, matrixPolls)
	reads, items, writes := sim.ledgerCounts()
	sessions := proxy.accepts.Load()
	if writes != 0 {
		t.Fatalf("ads: %d write services reached the controller", writes)
	}
	if reads != matrixPolls || items != matrixPolls*len(mc.tags) {
		t.Fatalf("ads: expected one sum-read per poll (%d requests, %d items), got %d requests, %d items", matrixPolls, matrixPolls*len(mc.tags), reads, items)
	}
	if sessions != 1 {
		t.Fatalf("ads: expected one persistent session across polls, got %d", sessions)
	}
	reconnected := reconnectAfterReset(t, proxy, endpoint, mc)
	if _, _, writes := sim.ledgerCounts(); writes != 0 {
		t.Fatalf("ads: %d write services after reconnect", writes)
	}
	recordEvidence(t, protocolEvidence{Protocol: "ads", Simulator: "pyads 3.6.0 AdsTestServer + Hades handler", Endpoint: endpoint, Values: values, Polls: matrixPolls, Tags: len(mc.tags), ReadRequests: reads, ReadItems: items, TCPSessions: sessions, Reconnected: reconnected, ElapsedMS: float64(elapsed.Microseconds()) / 1000})
}

func TestSimulatorBACnet(t *testing.T) {
	requireSimulators(t)
	port := freePort(t, "udp")
	sim, _ := pythonFixture(t, "bacnet_server.py", port)
	endpoint := fmt.Sprintf("bacnet-ip://127.0.0.1:%d?local-port=%d", port, freePort(t, "udp"))
	mc := matrixCase{protocol: "bacnet-ip", tags: []PLC4GoTag{
		{Name: "analog", Address: "ANALOG_VALUE,1/PRESENT_VALUE"},
		{Name: "binary", Address: "BINARY_VALUE,1/PRESENT_VALUE"},
		{Name: "integer", Address: "INTEGER_VALUE,1/PRESENT_VALUE"},
		{Name: "positive", Address: "POSITIVE_INTEGER_VALUE,1/PRESENT_VALUE"},
	}, want: map[string]any{"analog": 3.25, "binary": 1, "integer": 4200, "positive": 4242}}
	values, elapsed := pollMatrix(t, endpoint, mc, matrixPolls)
	reads, items, writes := sim.ledgerCounts()
	if writes != 0 {
		t.Fatalf("bacnet: %d write services reached the device", writes)
	}
	if reads == 0 || items < matrixPolls*len(mc.tags) {
		t.Fatalf("bacnet: expected every tag read each poll, got %d requests, %d items", reads, items)
	}
	recordEvidence(t, protocolEvidence{Protocol: "bacnet-ip", Simulator: "bacpypes3 0.0.110", Endpoint: endpoint, Values: values, Polls: matrixPolls, Tags: len(mc.tags), ReadRequests: reads, ReadItems: items, ElapsedMS: float64(elapsed.Microseconds()) / 1000})
}

// seedLogixTag writes one value to ab_server over a direct, test-only CIP
// connection that bypasses the inspected proxy (ab_server starts every tag at
// zero and has no option for initial values). The gateway path stays read-only.
func seedLogixTag(t *testing.T, address, tag string, cipType uint16, data []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	send := func(command uint16, session uint32, payload []byte) []byte {
		frame := make([]byte, 24, 24+len(payload))
		binary.LittleEndian.PutUint16(frame[0:2], command)
		binary.LittleEndian.PutUint16(frame[2:4], uint16(len(payload)))
		binary.LittleEndian.PutUint32(frame[4:8], session)
		frame = append(frame, payload...)
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		header := make([]byte, 24)
		if _, err := io.ReadFull(conn, header); err != nil {
			t.Fatal(err)
		}
		if status := binary.LittleEndian.Uint32(header[8:12]); status != 0 {
			t.Fatalf("seed %s: encapsulation status %d", tag, status)
		}
		body := make([]byte, binary.LittleEndian.Uint16(header[2:4]))
		if _, err := io.ReadFull(conn, body); err != nil {
			t.Fatal(err)
		}
		if command == 0x65 {
			return header[4:8]
		}
		return body
	}
	session := binary.LittleEndian.Uint32(send(0x65, 0, []byte{1, 0, 0, 0})) // RegisterSession
	// Symbolic path (ANSI extended symbol segment), padded to a word boundary.
	path := append([]byte{0x91, byte(len(tag))}, tag...)
	if len(path)%2 == 1 {
		path = append(path, 0)
	}
	write := append([]byte{cipWriteTag, byte(len(path) / 2)}, path...)
	write = binary.LittleEndian.AppendUint16(write, cipType)
	write = binary.LittleEndian.AppendUint16(write, 1)
	write = append(write, data...)
	// Unconnected Send to the connection manager, routed to backplane port 1 slot 0.
	ucmm := []byte{cipUnconnectedSend, 0x02, 0x20, 0x06, 0x24, 0x01, 0x0A, 0x0E}
	ucmm = binary.LittleEndian.AppendUint16(ucmm, uint16(len(write)))
	ucmm = append(ucmm, write...)
	if len(write)%2 == 1 {
		ucmm = append(ucmm, 0)
	}
	ucmm = append(ucmm, 0x01, 0x00, 0x01, 0x00) // route path: 1 word, port 1 slot 0
	cpf := []byte{0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0xB2, 0}
	cpf = binary.LittleEndian.AppendUint16(cpf, uint16(len(ucmm)))
	cpf = append(cpf, ucmm...)
	reply := send(eipSendRRData, session, cpf)
	if len(reply) < 20 || reply[16] != cipWriteTag|0x80 || reply[18] != 0 {
		t.Fatalf("seed %s: unexpected CIP reply % x", tag, reply)
	}
}

func TestSimulatorEtherNetIP(t *testing.T) {
	requireSimulators(t)
	binary := os.Getenv("HADES_AB_SERVER")
	if binary == "" {
		t.Skip("set HADES_AB_SERVER to the binary built by scripts/build-ab-server")
	}
	port := freePort(t, "tcp")
	ledger := filepath.Join(t.TempDir(), "unused.jsonl")
	startSimulatorWhenListening(t, ledger, port, binary, "--plc=ControlLogix", "--path=1,0", fmt.Sprintf("--port=%d", port),
		"--tag=Temp:REAL[1]", "--tag=Count:DINT[1]", "--tag=Small:INT[1]", "--tag=Flag:BOOL[1]")
	direct := fmt.Sprintf("127.0.0.1:%d", port)
	seedLogixTag(t, direct, "Temp", 0xCA, binary32(math.Float32bits(3.25)))
	seedLogixTag(t, direct, "Count", 0xC4, binary32(0xFFFE1DC0)) // int32(-123456)
	seedLogixTag(t, direct, "Small", 0xC3, []byte{0xD6, 0xFF})   // -42
	seedLogixTag(t, direct, "Flag", 0xC1, []byte{1})
	counter := &cipServiceCounter{}
	proxy := startTCPProxy(t, direct, counter.feed)
	endpoint := fmt.Sprintf("logix://127.0.0.1:%d", proxy.port())
	mc := matrixCase{protocol: "logix", tags: []PLC4GoTag{
		{Name: "real", Address: "%Temp:REAL"}, {Name: "dint", Address: "%Count:DINT"},
		{Name: "int", Address: "%Small:INT"}, {Name: "bool", Address: "%Flag:BOOL"},
	}, want: map[string]any{"real": 3.25, "dint": -123456, "int": -42, "bool": 1}}
	values, elapsed := pollMatrix(t, endpoint, mc, matrixPolls)
	reads, items, writes := counter.counts()
	sessions := proxy.accepts.Load()
	if writes != 0 {
		t.Fatalf("logix: %d CIP write services on the wire", writes)
	}
	if items < matrixPolls*len(mc.tags) {
		t.Fatalf("logix: expected every tag read each poll, got %d requests, %d items", reads, items)
	}
	if sessions != 1 {
		t.Fatalf("logix: expected one persistent session across polls, got %d", sessions)
	}
	reconnected := reconnectAfterReset(t, proxy, endpoint, mc)
	if _, _, writes := counter.counts(); writes != 0 {
		t.Fatalf("logix: %d CIP write services after reconnect", writes)
	}
	recordEvidence(t, protocolEvidence{Protocol: "logix", Simulator: "libplctag ab_server v2.7.2", Endpoint: endpoint, Values: values, Polls: matrixPolls, Tags: len(mc.tags), ReadRequests: reads, ReadItems: items, TCPSessions: sessions, Reconnected: reconnected, ElapsedMS: float64(elapsed.Microseconds()) / 1000})
}

func binary32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// startSimulatorWhenListening starts a fixture that prints no ready line and
// waits until its TCP port accepts connections.
func startSimulatorWhenListening(t *testing.T, ledger string, port int, name string, args ...string) *simulator {
	t.Helper()
	cmd := exec.Command(name, args...)
	output := &strings.Builder{}
	cmd.Stdout = &lockedWriter{w: output}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	s := &simulator{t: t, cmd: cmd, ledger: ledger, output: output}
	t.Cleanup(s.stop)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			conn.Close()
			return s
		}
	}
	t.Fatalf("%s did not listen on %d\n%s", name, port, output)
	return nil
}

// TestCIPWireCounterDetectsWrites is the negative control for the EtherNet/IP
// no-write proof: a real Write Tag through the inspected proxy must be counted.
func TestCIPWireCounterDetectsWrites(t *testing.T) {
	requireSimulators(t)
	binary := os.Getenv("HADES_AB_SERVER")
	if binary == "" {
		t.Skip("set HADES_AB_SERVER to the binary built by scripts/build-ab-server")
	}
	port := freePort(t, "tcp")
	startSimulatorWhenListening(t, filepath.Join(t.TempDir(), "unused.jsonl"), port, binary, "--plc=ControlLogix", "--path=1,0", fmt.Sprintf("--port=%d", port), "--tag=Count:DINT[1]")
	counter := &cipServiceCounter{}
	proxy := startTCPProxy(t, fmt.Sprintf("127.0.0.1:%d", port), counter.feed)
	seedLogixTag(t, fmt.Sprintf("127.0.0.1:%d", proxy.port()), "Count", 0xC4, binary32(7))
	time.Sleep(100 * time.Millisecond)
	if _, _, writes := counter.counts(); writes != 1 {
		t.Fatalf("expected the wire counter to detect exactly one CIP write, got %d", writes)
	}
}

// TestFixtureLedgersDetectWrites is the negative control for the S7, ADS and
// BACnet no-write proofs: one real write per protocol must reach the ledger.
func TestFixtureLedgersDetectWrites(t *testing.T) {
	requireSimulators(t)
	python := os.Getenv("HADES_PLC_PYTHON")
	if python == "" {
		python = "python3"
	}
	for _, tc := range []struct{ protocol, script, network string }{
		{"s7", "s7_server.py", "tcp"}, {"ads", "ads_server.py", "tcp"}, {"bacnet", "bacnet_server.py", "udp"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			port := freePort(t, tc.network)
			sim, _ := pythonFixture(t, tc.script, port)
			probe := exec.Command(python, fixturePath("write_probe.py"), tc.protocol, strconv.Itoa(port))
			if output, err := probe.CombinedOutput(); err != nil {
				t.Fatalf("write probe: %v\n%s", err, output)
			}
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
				if _, _, writes := sim.ledgerCounts(); writes > 0 {
					return
				}
			}
			t.Fatalf("%s fixture did not record a write\n%s", tc.protocol, sim.output)
		})
	}
}

// TestBACnetNegativeIntegerDecodeIsPinned guards a known plc4go defect at the
// pinned commit: BACnet SignedInteger present values are not sign-extended
// (-123456 decodes as 16653760, the 24-bit payload 0xFE1DC0). Production maps
// must not rely on negative BACnet IntegerValue readings. When a plc4go update
// fixes the decoder this test fails; then delete it and the documented limit.
func TestBACnetNegativeIntegerDecodeIsPinned(t *testing.T) {
	requireSimulators(t)
	port := freePort(t, "udp")
	pythonFixture(t, "bacnet_server.py", port)
	endpoint := fmt.Sprintf("bacnet-ip://127.0.0.1:%d?local-port=%d", port, freePort(t, "udp"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driver, err := OpenPLC4GoDriver(ctx, NewPLC4XConnector(), endpoint, []PLC4GoTag{{Name: "negative", Address: "INTEGER_VALUE,2/PRESENT_VALUE"}}, DriverOptions{ReadTimeout: 10 * time.Second, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	readings, _, reason := driver.Poll(ctx)
	if reason != "" {
		t.Fatal(reason)
	}
	if sameNumber(readings["negative"].Value, -123456) {
		t.Fatal("plc4go now sign-extends BACnet SignedInteger: remove this guard and the documented limitation")
	}
	if !sameNumber(readings["negative"].Value, 16653760) {
		t.Fatalf("unexpected decode of -123456: %#v", readings["negative"].Value)
	}
}

// TestSimulatorScanBudgetAtDefaultGuardrails polls the S7 simulator as fast as
// the caller can for a fixed window with default guardrails (1 request/s, burst
// 1, one in flight) and measures what actually reaches the controller.
func TestSimulatorScanBudgetAtDefaultGuardrails(t *testing.T) {
	requireSimulators(t)
	port := freePort(t, "tcp")
	sim, _ := pythonFixture(t, "s7_server.py", port)
	endpoint := fmt.Sprintf("s7://127.0.0.1:%d", port)
	tags := []PLC4GoTag{{Name: "real", Address: "%DB1:0:REAL"}, {Name: "dint", Address: "%DB1:4:DINT"}, {Name: "uint", Address: "%DB1:10:UINT"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	driver, err := OpenPLC4GoDriver(ctx, NewPLC4XConnector(), endpoint, tags, DriverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	const window = 3 * time.Second
	attempts, admitted, skipped := 0, 0, 0
	for started := time.Now(); time.Since(started) < window; attempts++ {
		_, _, reason := driver.Poll(ctx)
		switch reason {
		case "":
			admitted++
		case "scan-protection":
			skipped++
		default:
			t.Fatalf("unexpected poll failure: %s", reason)
		}
		time.Sleep(10 * time.Millisecond)
	}
	reads, items, writes := sim.ledgerCounts()
	// A burst of 1 plus 1 token/s over a 3 s window admits at most 4 polls.
	limit := 1 + int(window.Seconds())
	if admitted > limit || reads != admitted || writes != 0 {
		t.Fatalf("attempts=%d admitted=%d (limit %d) controller reads=%d writes=%d", attempts, admitted, limit, reads, writes)
	}
	recordEvidence(t, protocolEvidence{Protocol: "s7-scan-budget", Simulator: "python-snap7 3.2.1", Endpoint: endpoint,
		Values: map[string]any{"caller_attempts": attempts, "admitted_polls": admitted, "skipped_scan_protection": skipped, "window_seconds": window.Seconds(), "admitted_limit": limit},
		Polls:  admitted, Tags: len(tags), ReadRequests: reads, ReadItems: items})
}

func TestCIPServiceCounterClassifiesNestedServices(t *testing.T) {
	symbol := []byte{0x91, 0x04, 'T', 'e', 'm', 'p'} // 3-word ANSI symbol path
	request := func(service byte, path []byte, body ...byte) []byte {
		return append(append([]byte{service, byte(len(path) / 2)}, path...), body...)
	}
	read := request(cipReadTag, symbol, 1, 0)
	write := request(cipWriteTag, symbol, 0xC4, 0, 1, 0, 7, 0, 0, 0)
	cm := []byte{0x20, 0x06, 0x24, 0x01}
	ucmm := func(inner []byte) []byte {
		body := []byte{0x0A, 0x0E}
		body = binary.LittleEndian.AppendUint16(body, uint16(len(inner)))
		return request(cipUnconnectedSend, cm, append(body, inner...)...)
	}
	msp := func(members ...[]byte) []byte {
		body := binary.LittleEndian.AppendUint16(nil, uint16(len(members)))
		offset := 2 + 2*len(members)
		for _, m := range members {
			body = binary.LittleEndian.AppendUint16(body, uint16(offset))
			offset += len(m)
		}
		for _, m := range members {
			body = append(body, m...)
		}
		return request(cipMultipleService, []byte{0x20, 0x02, 0x24, 0x01}, body...)
	}
	cases := []struct {
		name                string
		frame               []byte
		reads, items, wants int
	}{
		{"read tag", read, 1, 1, 0},
		{"write tag", write, 0, 0, 1},
		{"write inside unconnected send", ucmm(write), 0, 0, 1},
		{"read fragment on a non connection-manager 2-word path", request(cipReadTagFragment, []byte{0x20, 0x6B, 0x25, 0x00, 0x01, 0x00}, 1, 0, 0, 0, 0, 0), 1, 1, 0},
		{"reads inside multiple service packet", msp(read, read, read), 1, 3, 0},
		{"write inside multiple service packet", msp(read, write), 1, 1, 1},
		{"write in unconnected send inside multiple service packet", msp(read, ucmm(write)), 1, 1, 1},
		{"multiple service packet inside unconnected send", ucmm(msp(read, write)), 1, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter := &cipServiceCounter{}
			counter.classifyCIP(tc.frame, 0)
			reads, items, writes := counter.counts()
			if reads != tc.reads || items != tc.items || writes != tc.wants {
				t.Fatalf("reads=%d items=%d writes=%d, want %d %d %d", reads, items, writes, tc.reads, tc.items, tc.wants)
			}
		})
	}
}
