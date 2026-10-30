package gateway

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// modbusRegisterServer is a loopback Modbus TCP holding-register server that
// records every request PDU and every accepted connection.
type modbusRegisterServer struct {
	listener net.Listener
	mu       sync.Mutex
	requests [][2]uint16 // start, count
	accepts  int
	writes   int
}

func startModbusRegisterServer(t *testing.T) *modbusRegisterServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &modbusRegisterServer{listener: listener}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			server.accepts++
			server.mu.Unlock()
			go server.serve(conn)
		}
	}()
	return server
}

func (s *modbusRegisterServer) serve(conn net.Conn) {
	defer conn.Close()
	for {
		header := make([]byte, 7)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		pdu := make([]byte, int(binary.BigEndian.Uint16(header[4:6]))-1)
		if _, err := io.ReadFull(conn, pdu); err != nil {
			return
		}
		if pdu[0] != 0x03 {
			s.mu.Lock()
			s.writes++
			s.mu.Unlock()
			conn.Write(append(append([]byte{}, header[:4]...), 0, 3, header[6], pdu[0]|0x80, 0x01))
			continue
		}
		start, count := binary.BigEndian.Uint16(pdu[1:3]), binary.BigEndian.Uint16(pdu[3:5])
		s.mu.Lock()
		s.requests = append(s.requests, [2]uint16{start, count})
		s.mu.Unlock()
		response := make([]byte, 9+2*count)
		copy(response, header[:4])
		binary.BigEndian.PutUint16(response[4:6], uint16(3+2*count))
		response[6], response[7], response[8] = header[6], 0x03, byte(2*count)
		for i := uint16(0); i < count; i++ {
			binary.BigEndian.PutUint16(response[9+2*i:], 100+start+i)
		}
		conn.Write(response)
	}
}

func TestPLC4XModbusDriverCoalescesAndReusesSession(t *testing.T) {
	server := startModbusRegisterServer(t)
	endpoint := "modbus-tcp://" + server.listener.Addr().String() + "?request-timeout-ms=1000"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// PLC4X Modbus addresses are one-based: holding-register:11 is wire address 10.
	driver, err := OpenPLC4GoDriver(ctx, NewPLC4XConnector(), endpoint, []PLC4GoTag{
		{Name: "temperature", Address: "holding-register:11:UINT", Unit: "deciCel"},
		{Name: "pressure", Address: "holding-register:12:UINT", Unit: "bar"},
		{Name: "speed", Address: "holding-register:13:UINT", Unit: "1"},
	}, DriverOptions{ReadTimeout: 2 * time.Second, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	for poll := range 3 {
		readings, _, reason := driver.Poll(ctx)
		if reason != "" {
			t.Fatalf("poll %d: %s", poll, reason)
		}
		for name, want := range map[string]uint16{"temperature": 110, "pressure": 111, "speed": 112} {
			if readings[name].Quality != "good" || readings[name].Value != want {
				t.Fatalf("poll %d: %s=%+v, want %d", poll, name, readings[name], want)
			}
		}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.accepts != 1 {
		t.Fatalf("expected one persistent TCP session across polls, got %d", server.accepts)
	}
	if server.writes != 0 {
		t.Fatal("read path must never issue a non-read function code")
	}
	if len(server.requests) != 3 {
		t.Fatalf("expected one coalesced PDU per poll, got %v", server.requests)
	}
	for _, request := range server.requests {
		if request != [2]uint16{10, 3} {
			t.Fatalf("expected registers 10..12 in one read, got %v", server.requests)
		}
	}
}

func TestPLC4XProfileAddressesAreOneBased(t *testing.T) {
	server := startModbusRegisterServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	driver, err := OpenPLC4GoDriverForProfile(ctx, NewPLC4XConnector(), "modbus-tcp://"+server.listener.Addr().String(), DefaultLabProfile(), DriverOptions{ReadTimeout: 2 * time.Second, RatePerSecond: 100, Burst: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	readings, _, reason := driver.Poll(ctx)
	if reason != "" || readings["temperature"].Value != uint16(100) {
		t.Fatalf("expected profile register 0 to read wire address 0: reason=%q value=%T(%v)", reason, readings["temperature"].Value, readings["temperature"].Value)
	}
}

func TestPLC4XConnectionStringRejectsUnsupportedEndpoints(t *testing.T) {
	for _, endpoint := range []string{"opc.tcp://127.0.0.1:4840", "s7://", "modbus-tcp://user@127.0.0.1:502", "knxnet-ip://127.0.0.1"} {
		if _, err := plc4xConnectionString(endpoint); err == nil {
			t.Fatalf("expected %q to be rejected", endpoint)
		}
	}
	for _, endpoint := range []string{"s7://10.0.0.5?remote-rack=0&remote-slot=1", "eip://10.0.0.6", "ads://10.0.0.7", "bacnet-ip://10.0.0.8", "modbus-tcp://10.0.0.9:502"} {
		if _, err := plc4xConnectionString(endpoint); err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
	}
}
