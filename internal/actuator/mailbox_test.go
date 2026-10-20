package actuator

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// The loopback fixture represents PLC-owned acceptance logic, not an edge output writer.
func TestRealTCPMailboxHandshake(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "executed", true: "interlock-refused"}[refused], func(t *testing.T) {
			f := setup(t, "L2")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var mu sync.Mutex
			registers := map[uint16]uint16{103: 4}
			writes := []uint16{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					func() {
						defer conn.Close()
						conn.SetDeadline(time.Now().Add(time.Second))
						packet := make([]byte, 12)
						if _, err := io.ReadFull(conn, packet); err != nil {
							return
						}
						addr := binary.BigEndian.Uint16(packet[8:])
						value := binary.BigEndian.Uint16(packet[10:])
						mu.Lock()
						defer mu.Unlock()
						if packet[7] == 6 {
							writes = append(writes, addr)
							registers[addr] = value
							if addr == 101 {
								registers[105] = value
								if refused {
									registers[104] = 9
								} else {
									registers[103] = registers[100]
									registers[104] = 0
								}
							}
							conn.Write(packet)
						} else if packet[7] == 3 {
							response := append([]byte(nil), packet[:7]...)
							binary.BigEndian.PutUint16(response[4:], 5)
							response = append(response, 3, 2, byte(registers[addr]>>8), byte(registers[addr]))
							conn.Write(response)
						}
					}()
				}
			}()
			key := f.o.Bindings[0].Asset + "/motor_temperature"
			b := f.e.bindings[key]
			b.Endpoint = listener.Addr().String()
			f.e.bindings[key] = b
			f.e.opts.PLC = &ModbusMailbox{}
			receipt, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false)
			want := "EXECUTED"
			if refused {
				want = "PLC_INTERLOCK_REFUSED"
			}
			if err != nil || state(t, receipt) != want {
				t.Fatal(err, string(receipt))
			}
			mu.Lock()
			for _, a := range writes {
				if a != 100 && a != 101 && a != 102 {
					t.Fatal("non-staging write", a)
				}
			}
			if len(writes) != 3 {
				t.Fatal(writes)
			}
			mu.Unlock()
			listener.Close()
			<-done
		})
	}
}
func TestMailboxRejectsAliasedAndPhysicalOutputBindings(t *testing.T) {
	b := Binding{Endpoint: "127.0.0.1:1502", AckSequence: "holding:105"}
	m := Mailbox{"holding:100", "holding:101", "holding:102", "holding:103", "holding:104"}
	if err := ValidateBinding(b, m); err != nil {
		t.Fatal(err)
	}
	m.Active = "holding:00100"
	if ValidateBinding(b, m) == nil {
		t.Fatal("numeric alias accepted")
	}
	m.Active = "Q:0"
	if ValidateBinding(b, m) == nil {
		t.Fatal("physical output accepted")
	}
	m.Active = "holding:103"
	b.Endpoint = "192.0.2.1:502"
	if ValidateBinding(b, m) == nil {
		t.Fatal("real device enabled without site qualification")
	}
}
