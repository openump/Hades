package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PLC4GoTag struct {
	Name    string
	Address string
	Unit    string
}

// PLC4GoReadRequest executes one multi-tag read. Values are numeric Go values
// (uint16 for raw registers, wider types for typed S7/EIP/ADS/BACnet tags).
// Tags the controller rejected are absent from the result.
type PLC4GoReadRequest interface {
	Execute(ctx context.Context) (map[string]any, error)
}

type PLC4GoReadRequestBuilder interface {
	AddTagAddress(name, address string) error
	Build() (PLC4GoReadRequest, error)
}

type PLC4GoConnection interface {
	ReadRequestBuilder() PLC4GoReadRequestBuilder
	Close() error
}

type PLC4GoConnector interface {
	Connect(ctx context.Context, endpoint string) (PLC4GoConnection, error)
}

// coalescingConnector is implemented by connectors whose protocol drivers merge
// adjacent tags of one read request into contiguous PDUs themselves (the PLC4X
// read optimizer). The driver then sends every tag in a single request.
type coalescingConnector interface {
	CoalescesReads() bool
}

// modbusTagFormatter is implemented by connectors whose Modbus register address
// syntax differs from the zero-based "holding-register:N" default.
type modbusTagFormatter interface {
	ModbusHoldingRegisterTag(register RegisterMapping) string
}

type PLC4GoDriver struct {
	endpoint   string
	tags       []PLC4GoTag
	batches    [][]PLC4GoTag
	connector  PLC4GoConnector
	connMu     sync.Mutex
	connection PLC4GoConnection
	closed     bool
	options    DriverOptions
	session    *PersistentSession
	guardrail  *EndpointGuardrail
	status     *statusEmitter
	deadbands  deadbandSet
}

func OpenPLC4GoDriver(ctx context.Context, connector PLC4GoConnector, endpoint string, tags []PLC4GoTag, options DriverOptions) (*PLC4GoDriver, error) {
	if connector == nil {
		return nil, errors.New("plc4go connector is required")
	}
	if len(tags) == 0 {
		return nil, errors.New("plc4go tags are required")
	}
	if err := validatePLC4GoEndpoint(endpoint); err != nil {
		return nil, err
	}
	connection, err := connector.Connect(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	batches := buildPLC4GoBatches(endpoint, tags)
	if c, ok := connector.(coalescingConnector); ok && c.CoalescesReads() {
		batches = [][]PLC4GoTag{append([]PLC4GoTag(nil), tags...)}
	}
	options = normalizeDriverOptions(options)
	return &PLC4GoDriver{
		endpoint:   endpoint,
		tags:       append([]PLC4GoTag(nil), tags...),
		batches:    batches,
		connector:  connector,
		connection: connection,
		options:    options,
		session:    NewPersistentSession(endpoint),
		guardrail:  NewEndpointGuardrail(endpoint, options.RatePerSecond, options.Burst, options.MaxInFlight),
		status:     newStatusEmitter(options),
		deadbands:  newDeadbandSet(options.Deadbands),
	}, nil
}

// OpenPLC4GoDriverForProfile maps a zero-based Modbus register profile to tags,
// using the connector's own address syntax when it has one.
func OpenPLC4GoDriverForProfile(ctx context.Context, connector PLC4GoConnector, endpoint string, profile Profile, options DriverOptions) (*PLC4GoDriver, error) {
	formatter, custom := connector.(modbusTagFormatter)
	tags := make([]PLC4GoTag, 0, len(profile.Registers))
	for _, register := range profile.Registers {
		address := fmt.Sprintf("holding-register:%d", register.Address)
		if custom {
			address = formatter.ModbusHoldingRegisterTag(register)
		}
		tags = append(tags, PLC4GoTag{
			Name:    register.Name,
			Address: address,
			Unit:    register.Unit,
		})
	}
	return OpenPLC4GoDriver(ctx, connector, endpoint, tags, options)
}

func (d *PLC4GoDriver) Poll(ctx context.Context) (map[string]Reading, time.Time, string) {
	if err := d.guardrail.Enter(); err != nil {
		reason := guardReason(err)
		return d.deadbands.filter(d.failureReadings(reason), d.options.Now()), d.options.Now(), reason
	}
	defer d.guardrail.Leave()

	readCtx, cancel := context.WithTimeout(ctx, d.options.ReadTimeout)
	defer cancel()

	d.connMu.Lock()
	defer d.connMu.Unlock()
	readings := make(map[string]Reading, len(d.tags))
	for _, batch := range d.batches {
		batchReadings, err := d.readBatch(readCtx, batch)
		if err != nil {
			// A timed-out or reset session may still deliver a stale response;
			// drop it so the next poll starts from a fresh connection.
			d.dropConnection()
			reason := "read-error"
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(readCtx.Err(), context.DeadlineExceeded) {
				reason = "read-timeout"
			}
			event := d.guardrail.RecordFailure(reason)
			d.status.emit(event)
			return d.deadbands.filter(d.failureReadings(reason), d.options.Now()), d.options.Now(), reason
		}
		for name, reading := range batchReadings {
			readings[name] = reading
		}
	}

	d.session.RecordPoll(plansForTags(d.batches), "")
	recovery := d.guardrail.RecordSuccess()
	d.status.emit(recovery)
	stamp := d.options.Now()
	return d.deadbands.filter(readings, stamp), stamp, ""
}

func (d *PLC4GoDriver) Close() error {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	d.closed = true
	if d.connection == nil {
		return nil
	}
	err := d.connection.Close()
	d.connection = nil
	return err
}

func (d *PLC4GoDriver) dropConnection() {
	if d.connection != nil {
		_ = d.connection.Close()
		d.connection = nil
	}
}

func (d *PLC4GoDriver) readBatch(ctx context.Context, batch []PLC4GoTag) (map[string]Reading, error) {
	if d.closed {
		return nil, errors.New("plc4go driver is closed")
	}
	if d.connection == nil {
		connection, err := d.connector.Connect(ctx, d.endpoint)
		if err != nil {
			return nil, err
		}
		d.connection = connection
	}
	observed := d.options.Now()
	builder := d.connection.ReadRequestBuilder()
	for _, tag := range batch {
		if err := builder.AddTagAddress(tag.Name, tag.Address); err != nil {
			return nil, err
		}
	}
	request, err := builder.Build()
	if err != nil {
		return nil, err
	}
	values, err := request.Execute(ctx)
	if err != nil {
		return nil, err
	}
	readings := make(map[string]Reading, len(batch))
	for _, tag := range batch {
		value, ok := values[tag.Name]
		if !ok {
			readings[tag.Name] = Reading{Value: nil, Unit: tag.Unit, Quality: "bad", Reason: "missing-tag", ObservedAt: observed}
			continue
		}
		readings[tag.Name] = Reading{
			Value:      value,
			Unit:       tag.Unit,
			Quality:    "good",
			ObservedAt: observed,
			Extensions: map[string]any{"address": tag.Address},
		}
	}
	return readings, nil
}

func (d *PLC4GoDriver) failureReadings(reason string) map[string]Reading {
	out := make(map[string]Reading, len(d.tags))
	for _, tag := range d.tags {
		out[tag.Name] = Reading{Value: nil, Unit: tag.Unit, Quality: "bad", Reason: reason, ObservedAt: d.options.Now()}
	}
	return out
}

func validatePLC4GoEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	switch parsed.Scheme {
	case "modbus-tcp", "s7", "eip", "ads", "bacnet-ip":
		return nil
	default:
		return fmt.Errorf("unsupported plc4go scheme %q", parsed.Scheme)
	}
}

func buildPLC4GoBatches(endpoint string, tags []PLC4GoTag) [][]PLC4GoTag {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return [][]PLC4GoTag{append([]PLC4GoTag(nil), tags...)}
	}
	if parsed.Scheme != "modbus-tcp" && parsed.Scheme != "s7" {
		return [][]PLC4GoTag{append([]PLC4GoTag(nil), tags...)}
	}
	batches := make([][]PLC4GoTag, 0, len(tags))
	current := make([]PLC4GoTag, 0, len(tags))
	var prevKey string
	var prevIndex int
	for _, tag := range tags {
		key, index, ok := plc4goCoalesceKey(parsed.Scheme, tag.Address)
		if !ok {
			if len(current) > 0 {
				batches = append(batches, current)
				current = nil
			}
			batches = append(batches, []PLC4GoTag{tag})
			prevKey = ""
			prevIndex = 0
			continue
		}
		if len(current) == 0 || key != prevKey || index != prevIndex+1 {
			if len(current) > 0 {
				batches = append(batches, current)
			}
			current = []PLC4GoTag{tag}
		} else {
			current = append(current, tag)
		}
		prevKey = key
		prevIndex = index
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func plc4goCoalesceKey(scheme, address string) (string, int, bool) {
	switch scheme {
	case "modbus-tcp":
		parts := strings.Split(address, ":")
		if len(parts) != 2 {
			return "", 0, false
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, false
		}
		return parts[0], index, true
	case "s7":
		parts := strings.Split(address, ":")
		if len(parts) != 2 {
			return "", 0, false
		}
		index, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, false
		}
		return parts[0], index, true
	default:
		return "", 0, false
	}
}

func plansForTags(batches [][]PLC4GoTag) []ReadRequestPlan {
	plans := make([]ReadRequestPlan, 0, len(batches))
	for _, batch := range batches {
		registers := make([]RegisterMapping, 0, len(batch))
		for index, tag := range batch {
			registers = append(registers, RegisterMapping{Name: tag.Name, Address: uint16(index), Unit: tag.Unit})
		}
		plans = append(plans, ReadRequestPlan{Registers: registers, StartAddress: 0, EndAddress: uint16(len(batch) - 1)})
	}
	return plans
}

type ModbusTCPPLC4GoConnector struct {
	dialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

func NewModbusTCPPLC4GoConnector() *ModbusTCPPLC4GoConnector {
	return &ModbusTCPPLC4GoConnector{
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}
}

type modbusTCPPLC4GoConnection struct {
	conn   net.Conn
	unitID uint8
	mu     sync.Mutex
	txID   uint16
}

func (c *ModbusTCPPLC4GoConnector) Connect(ctx context.Context, endpoint string) (PLC4GoConnection, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "modbus-tcp" {
		return nil, fmt.Errorf("unsupported plc4go scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := parsed.Port()
	if port == "" {
		port = "502"
	}
	conn, err := c.dialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	unitID := uint8(1)
	if raw := parsed.Query().Get("unit"); raw != "" {
		parsedUnit, err := strconv.ParseUint(raw, 10, 8)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("invalid plc4go modbus unit %q: %w", raw, err)
		}
		unitID = uint8(parsedUnit)
	}
	return &modbusTCPPLC4GoConnection{conn: conn, unitID: unitID}, nil
}

func (c *modbusTCPPLC4GoConnection) ReadRequestBuilder() PLC4GoReadRequestBuilder {
	return &modbusTCPPLC4GoReadRequestBuilder{connection: c}
}

func (c *modbusTCPPLC4GoConnection) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *modbusTCPPLC4GoConnection) nextTransactionID() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.txID++
	if c.txID == 0 {
		c.txID = 1
	}
	return c.txID
}

type modbusTCPPLC4GoReadRequestBuilder struct {
	connection *modbusTCPPLC4GoConnection
	tags       []modbusTCPPLC4GoTag
}

type modbusTCPPLC4GoTag struct {
	name    string
	address uint16
}

func (b *modbusTCPPLC4GoReadRequestBuilder) AddTagAddress(name, address string) error {
	if name == "" {
		return errors.New("plc4go tag name is required")
	}
	addr, err := parsePLC4GoHoldingRegisterAddress(address)
	if err != nil {
		return err
	}
	b.tags = append(b.tags, modbusTCPPLC4GoTag{name: name, address: addr})
	return nil
}

func (b *modbusTCPPLC4GoReadRequestBuilder) Build() (PLC4GoReadRequest, error) {
	if len(b.tags) == 0 {
		return nil, errors.New("plc4go read request requires at least one tag")
	}
	groups := groupPLC4GoModbusTags(b.tags)
	return &modbusTCPPLC4GoReadRequest{connection: b.connection, groups: groups}, nil
}

type modbusTCPPLC4GoReadRequest struct {
	connection *modbusTCPPLC4GoConnection
	groups     [][]modbusTCPPLC4GoTag
}

func (r *modbusTCPPLC4GoReadRequest) Execute(ctx context.Context) (map[string]any, error) {
	values := make(map[string]any, len(r.groups))
	for _, group := range r.groups {
		if len(group) == 0 {
			continue
		}
		startAddr := group[0].address
		count := uint16(len(group))
		transactionID := r.connection.nextTransactionID()
		request, err := buildModbusReadHoldingRegistersRequest(transactionID, uint16(r.connection.unitID), startAddr, count)
		if err != nil {
			return nil, err
		}
		if err := writeModbusRequest(ctx, r.connection.conn, request); err != nil {
			return nil, err
		}
		response, err := readModbusHoldingRegisterResponse(ctx, r.connection.conn, transactionID, r.connection.unitID, count)
		if err != nil {
			return nil, err
		}
		for i, tag := range group {
			values[tag.name] = response[i]
		}
	}
	return values, nil
}

func parsePLC4GoHoldingRegisterAddress(address string) (uint16, error) {
	const prefix = "holding-register:"
	if !strings.HasPrefix(address, prefix) {
		return 0, fmt.Errorf("unsupported plc4go tag address %q", address)
	}
	idx, err := strconv.ParseUint(strings.TrimPrefix(address, prefix), 10, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid plc4go holding register address %q: %w", address, err)
	}
	return uint16(idx), nil
}

func groupPLC4GoModbusTags(tags []modbusTCPPLC4GoTag) [][]modbusTCPPLC4GoTag {
	if len(tags) == 0 {
		return nil
	}
	sorted := append([]modbusTCPPLC4GoTag(nil), tags...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].address < sorted[j].address
	})
	groups := make([][]modbusTCPPLC4GoTag, 0, len(sorted))
	current := make([]modbusTCPPLC4GoTag, 0, len(sorted))
	for _, tag := range sorted {
		if len(current) == 0 {
			current = append(current, tag)
			continue
		}
		prev := current[len(current)-1].address
		if tag.address == prev+1 {
			current = append(current, tag)
			continue
		}
		groups = append(groups, current)
		current = []modbusTCPPLC4GoTag{tag}
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func buildModbusReadHoldingRegistersRequest(transactionID, unitID uint16, startAddr, count uint16) ([]byte, error) {
	if count == 0 || count > 125 {
		return nil, fmt.Errorf("plc4go modbus read count must be 1..125, got %d", count)
	}
	request := make([]byte, 12)
	binary.BigEndian.PutUint16(request[0:2], transactionID)
	binary.BigEndian.PutUint16(request[2:4], 0)
	binary.BigEndian.PutUint16(request[4:6], 6)
	request[6] = byte(unitID)
	request[7] = 0x03
	binary.BigEndian.PutUint16(request[8:10], startAddr)
	binary.BigEndian.PutUint16(request[10:12], count)
	return request, nil
}

func writeModbusRequest(ctx context.Context, conn net.Conn, request []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer conn.SetWriteDeadline(time.Time{})
	}
	for written := 0; written < len(request); {
		n, err := conn.Write(request[written:])
		if err != nil {
			return err
		}
		written += n
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// readModbusHoldingRegisterResponse reads one FC03 response and checks that it
// answers the request that was sent: same transaction, unit and register count.
func readModbusHoldingRegisterResponse(ctx context.Context, conn net.Conn, transactionID uint16, unitID uint8, count uint16) ([]uint16, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		defer conn.SetReadDeadline(time.Time{})
	}
	header := make([]byte, 7)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	responseLength := int(binary.BigEndian.Uint16(header[4:6]))
	// An exception PDU (function code + exception code) is the shortest valid frame.
	if responseLength < 3 || responseLength > 254 {
		return nil, fmt.Errorf("invalid modbus response length %d", responseLength)
	}
	remaining := make([]byte, responseLength-1)
	if _, err := io.ReadFull(conn, remaining); err != nil {
		return nil, err
	}
	if got := binary.BigEndian.Uint16(header[0:2]); got != transactionID {
		return nil, fmt.Errorf("modbus transaction id %d does not match request %d", got, transactionID)
	}
	if protocolID := binary.BigEndian.Uint16(header[2:4]); protocolID != 0 {
		return nil, fmt.Errorf("unexpected modbus protocol id %d", protocolID)
	}
	if header[6] != unitID {
		return nil, fmt.Errorf("unexpected modbus unit id %d", header[6])
	}
	if remaining[0] == 0x83 {
		return nil, fmt.Errorf("modbus exception code %d", remaining[1])
	}
	if remaining[0] != 0x03 {
		return nil, fmt.Errorf("unexpected modbus function code %d", remaining[0])
	}
	byteCount := int(remaining[1])
	if byteCount != 2*int(count) {
		return nil, fmt.Errorf("unexpected modbus byte count %d for %d registers", byteCount, count)
	}
	if len(remaining)-2 < byteCount {
		return nil, fmt.Errorf("modbus response missing %d register bytes", byteCount-(len(remaining)-2))
	}
	data := remaining[2 : 2+byteCount]
	values := make([]uint16, 0, len(data)/2)
	for i := 0; i < len(data); i += 2 {
		values = append(values, binary.BigEndian.Uint16(data[i:i+2]))
	}
	return values, nil
}
