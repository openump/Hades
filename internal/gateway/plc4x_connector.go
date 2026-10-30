//go:build plc4x

// The PLC4X connector links every protocol of plc4go's public drivers package
// (about 1.4M lines of generated code), so it is only built with -tags plc4x.

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	plc4go "github.com/apache/plc4x/plc4go/pkg/api"
	"github.com/apache/plc4x/plc4go/pkg/api/config"
	"github.com/apache/plc4x/plc4go/pkg/api/drivers"
	apiModel "github.com/apache/plc4x/plc4go/pkg/api/model"
	apiValues "github.com/apache/plc4x/plc4go/pkg/api/values"
	"github.com/rs/zerolog"
)

// PLC4XConnector connects through the in-process Apache PLC4X Go drivers
// (no JVM) for s7://, modbus-tcp://, eip://, ads:// and bacnet-ip:// endpoints.
// Only read request builders are exposed; write, subscription and browse
// builders of the underlying connection are never reachable from the gateway.
type PLC4XConnector struct {
	once    sync.Once
	manager plc4go.PlcDriverManager
}

func NewPLC4XConnector() *PLC4XConnector {
	return &PLC4XConnector{}
}

// CoalescesReads reports that PLC4X merges adjacent registers / data-block
// offsets of one request into contiguous PDUs, within protocol limits.
func (c *PLC4XConnector) CoalescesReads() bool { return true }

// ModbusHoldingRegisterTag renders a zero-based profile register as a PLC4X
// tag. PLC4X addresses Modbus registers one-based and defaults untyped
// registers to signed INT, so the address is shifted and the type kept.
func (c *PLC4XConnector) ModbusHoldingRegisterTag(register RegisterMapping) string {
	plcType := "UINT"
	if register.DataType == "INT16" {
		plcType = "INT"
	}
	return fmt.Sprintf("holding-register:%d:%s", int(register.Address)+1, plcType)
}

func (c *PLC4XConnector) driverManager() plc4go.PlcDriverManager {
	c.once.Do(func() {
		options := []config.WithOption{config.WithCustomLogger(zerolog.Nop())}
		c.manager = plc4go.NewPlcDriverManager(options...)
		drivers.RegisterS7Driver(c.manager, options...)
		drivers.RegisterModbusTcpDriver(c.manager, options...)
		drivers.RegisterEipDriver(c.manager, options...)
		drivers.RegisterAdsDriver(c.manager, options...)
		drivers.RegisterBacnetDriver(c.manager, options...)
	})
	return c.manager
}

func (c *PLC4XConnector) Connect(ctx context.Context, endpoint string) (PLC4GoConnection, error) {
	connectionString, err := plc4xConnectionString(endpoint)
	if err != nil {
		return nil, err
	}
	connection, err := c.driverManager().GetConnection(ctx, connectionString)
	if err != nil {
		return nil, err
	}
	return &plc4xConnection{connection: connection}, nil
}

// plc4xConnectionString maps a gateway endpoint to a PLC4X connection string.
// PLC4X names BACnet/IP "bacnet-ip" and EtherNet/IP "eip", matching the gateway
// schemes, so only scheme validation is needed.
func plc4xConnectionString(endpoint string) (string, error) {
	if err := validatePLC4GoEndpoint(endpoint); err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("plc4x endpoint requires scheme://host[:port][?options]")
	}
	return parsed.String(), nil
}

type plc4xConnection struct {
	connection plc4go.PlcConnection
}

func (c *plc4xConnection) ReadRequestBuilder() PLC4GoReadRequestBuilder {
	return &plc4xReadRequestBuilder{builder: c.connection.ReadRequestBuilder()}
}

func (c *plc4xConnection) Close() error {
	return c.connection.Close()
}

type plc4xReadRequestBuilder struct {
	builder apiModel.PlcReadRequestBuilder
}

func (b *plc4xReadRequestBuilder) AddTagAddress(name, address string) error {
	if name == "" || address == "" {
		return errors.New("plc4x tag name and address are required")
	}
	b.builder.AddTagAddress(name, address)
	return nil
}

func (b *plc4xReadRequestBuilder) Build() (PLC4GoReadRequest, error) {
	request, err := b.builder.Build()
	if err != nil {
		return nil, err
	}
	return &plc4xReadRequest{request: request}, nil
}

type plc4xReadRequest struct {
	request apiModel.PlcReadRequest
}

func (r *plc4xReadRequest) Execute(ctx context.Context) (map[string]any, error) {
	var result apiModel.PlcReadRequestResult
	select {
	case result = <-r.request.Execute(ctx):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if result == nil {
		return nil, errors.New("plc4x read returned no result")
	}
	if err := result.GetErr(); err != nil {
		return nil, err
	}
	response := result.GetResponse()
	values := make(map[string]any, len(response.GetTagNames()))
	for _, name := range response.GetTagNames() {
		if response.GetResponseCode(name) != apiModel.PlcResponseCode_OK {
			continue
		}
		if value, ok := plc4xNumeric(response.GetValue(name)); ok {
			values[name] = value
		}
	}
	return values, nil
}

// plc4xNumeric converts a scalar PLC value to the matching Go numeric type,
// keyed on the declared PLC type. The PlcValue Is* predicates report
// convertibility (a UINT of 110 "is" a bool), so they cannot select the type.
// Arrays, strings, times and structs are not acquired.
func plc4xNumeric(value apiValues.PlcValue) (any, bool) {
	if value == nil || value.IsNull() {
		return nil, false
	}
	switch value.GetPlcValueType() {
	case apiValues.BOOL:
		if value.GetBool() {
			return uint8(1), true
		}
		return uint8(0), true
	case apiValues.BYTE, apiValues.USINT:
		return value.GetUint8(), true
	case apiValues.WORD, apiValues.UINT:
		return value.GetUint16(), true
	case apiValues.DWORD, apiValues.UDINT:
		return value.GetUint32(), true
	case apiValues.LWORD, apiValues.ULINT:
		return value.GetUint64(), true
	case apiValues.SINT:
		return int64(value.GetInt8()), true
	case apiValues.INT:
		return int64(value.GetInt16()), true
	case apiValues.DINT:
		return value.GetInt32(), true
	case apiValues.LINT:
		return value.GetInt64(), true
	case apiValues.REAL:
		return float64(value.GetFloat32()), true
	case apiValues.LREAL:
		return value.GetFloat64(), true
	default:
		return nil, false
	}
}
