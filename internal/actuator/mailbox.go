package actuator

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mailbox struct {
	Proposed string `json:"proposed_value"`
	Request  string `json:"request_sequence"`
	Watchdog string `json:"watchdog_counter"`
	Active   string `json:"active_value"`
	Reject   string `json:"reject_code"`
}
type Binding struct {
	Asset       string `json:"asset"`
	Parameter   string `json:"parameter"`
	Endpoint    string `json:"endpoint"`
	AckSequence string `json:"ack_sequence"`
}
type PLC interface {
	Stage(context.Context, Binding, Mailbox, uint16, float64) error
	Readback(context.Context, Binding, Mailbox) (float64, uint16, uint16, error)
	Heartbeat(context.Context, Binding, Mailbox, uint16) error
}

// ModbusMailbox is deliberately restricted to literal loopback software pilots.
// Values are uint16 engineering units; no coil/output write API exists.
type ModbusMailbox struct {
	mu          sync.Mutex
	transaction uint16
}

func address(s string) (uint16, error) {
	if !strings.HasPrefix(s, "holding:") {
		return 0, fmt.Errorf("only holding mailbox registers supported")
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "holding:"), 10, 16)
	return uint16(n), err
}
func ValidateBinding(b Binding, m Mailbox) error {
	host, port, err := net.SplitHostPort(b.Endpoint)
	n, e := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || e != nil || n < 1 || n > 65535 || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("mailbox requires literal loopback IP:port")
	}
	seen := map[uint16]bool{}
	for _, s := range []string{m.Proposed, m.Request, m.Watchdog, m.Active, m.Reject, b.AckSequence} {
		a, err := address(s)
		if err != nil || seen[a] {
			return fmt.Errorf("invalid or overlapping mailbox roles")
		}
		seen[a] = true
	}
	return nil
}
func (p *ModbusMailbox) exchange(ctx context.Context, b Binding, fc byte, addr, value uint16) (uint16, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transaction++
	tx := p.transaction
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", b.Endpoint)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline := time.Now().Add(300 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	packet := make([]byte, 12)
	binary.BigEndian.PutUint16(packet, tx)
	binary.BigEndian.PutUint16(packet[4:], 6)
	packet[6] = 1
	packet[7] = fc
	binary.BigEndian.PutUint16(packet[8:], addr)
	binary.BigEndian.PutUint16(packet[10:], value)
	if _, err = io.Copy(conn, strings.NewReader(string(packet))); err != nil {
		return 0, err
	}
	header := make([]byte, 7)
	if _, err = io.ReadFull(conn, header); err != nil {
		return 0, err
	}
	length := binary.BigEndian.Uint16(header[4:])
	if binary.BigEndian.Uint16(header) != tx || header[2] != 0 || header[3] != 0 || header[6] != 1 || length < 2 || length > 6 {
		return 0, fmt.Errorf("invalid Modbus response")
	}
	body := make([]byte, int(length)-1)
	if _, err = io.ReadFull(conn, body); err != nil {
		return 0, err
	}
	if fc == 6 {
		if len(body) != 5 || body[0] != fc || binary.BigEndian.Uint16(body[1:]) != addr || binary.BigEndian.Uint16(body[3:]) != value {
			return 0, fmt.Errorf("write not acknowledged")
		}
		return value, nil
	}
	if len(body) != 4 || body[0] != 3 || body[1] != 2 {
		return 0, fmt.Errorf("read not acknowledged")
	}
	return binary.BigEndian.Uint16(body[2:]), nil
}
func (p *ModbusMailbox) Stage(ctx context.Context, b Binding, m Mailbox, seq uint16, value float64) error {
	if err := ValidateBinding(b, m); err != nil {
		return err
	}
	if value < 0 || value > 65535 || math.Trunc(value) != value {
		return fmt.Errorf("uint16 engineering-unit value required")
	}
	a, _ := address(m.Proposed)
	if _, err := p.exchange(ctx, b, 6, a, uint16(value)); err != nil {
		return err
	}
	a, _ = address(m.Request)
	_, err := p.exchange(ctx, b, 6, a, seq)
	return err
}
func (p *ModbusMailbox) Readback(ctx context.Context, b Binding, m Mailbox) (float64, uint16, uint16, error) {
	if err := ValidateBinding(b, m); err != nil {
		return 0, 0, 0, err
	}
	values := make([]uint16, 4)
	for i, s := range []string{b.AckSequence, m.Active, m.Reject, b.AckSequence} {
		a, _ := address(s)
		v, err := p.exchange(ctx, b, 3, a, 1)
		if err != nil {
			return 0, 0, 0, err
		}
		values[i] = v
	}
	if values[0] != values[3] {
		return 0, 0, 0, nil
	}
	return float64(values[1]), values[2], values[3], nil
}
func (p *ModbusMailbox) Heartbeat(ctx context.Context, b Binding, m Mailbox, counter uint16) error {
	if err := ValidateBinding(b, m); err != nil {
		return err
	}
	a, _ := address(m.Watchdog)
	_, err := p.exchange(ctx, b, 6, a, counter)
	return err
}
