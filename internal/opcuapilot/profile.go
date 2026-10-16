// Package opcuapilot is the fixed, loopback-only OPC UA software pilot profile.
package opcuapilot

import (
	"fmt"
	"github.com/awcullen/opcua/ua"
	"math"
	"net"
	"net/url"
	"strconv"
	"time"
)

const NamespaceURI = "urn:openump:pilot:mock:v1"
const ServerURI = "urn:openump:opcua:mock-server"
const ClientURI = "urn:openump:opcua:read-only-gateway"
const Policy = ua.SecurityPolicyURIAes256Sha256RsaPss

type Signal struct {
	Name, Node, Unit string
	Type             ua.NodeID
	Min, Max         *float64
}

func floatPtr(v float64) *float64 { return &v }

var Signals = [...]Signal{
	{Name: "motor_temperature", Node: "Temperature", Unit: "Cel", Type: ua.DataTypeIDDouble, Min: floatPtr(-40), Max: floatPtr(150)},
	{Name: "cycle_counter", Node: "CycleCounter", Unit: "1", Type: ua.DataTypeIDUInt32},
	{Name: "machine_state", Node: "MachineState", Unit: "1", Type: ua.DataTypeIDUInt16, Min: floatPtr(0), Max: floatPtr(2)},
}

func ValidateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	ip := net.ParseIP(u.Hostname())
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || ip == nil || !ip.IsLoopback() || u.Scheme != "opc.tcp" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("software pilot requires literal loopback opc.tcp://IP:port")
	}
	return nil
}

func inBounds(v float64, min, max *float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	if min != nil && v < *min {
		return false
	}
	if max != nil && v > *max {
		return false
	}
	return true
}

// One envelope per node preserves that node's timestamp. OPC UA source time wins,
// then server time, then gateway completion; exact upstream timestamps remain in
// extensions while the v1 envelope uses its required millisecond representation.
func MapValue(signal Signal, value ua.DataValue, now time.Time) (map[string]any, time.Time, string, map[string]any) {
	stamp, origin := value.SourceTimestamp, "device"
	if stamp.IsZero() {
		stamp = value.ServerTimestamp
	}
	if stamp.IsZero() {
		stamp = now
		origin = "gateway"
	}
	ext := map[string]any{"opcua.node_id": signal.Node, "opcua.status_code": uint32(value.StatusCode), "opcua.source_timestamp": value.SourceTimestamp.UTC().Format(time.RFC3339Nano), "opcua.server_timestamp": value.ServerTimestamp.UTC().Format(time.RFC3339Nano)}
	if value.SourceTimestamp.IsZero() {
		ext["opcua.source_timestamp"] = nil
	}
	if value.ServerTimestamp.IsZero() {
		ext["opcua.server_timestamp"] = nil
	}
	ext["opcua.source_picoseconds"] = value.SourcePicoseconds
	ext["opcua.server_picoseconds"] = value.ServerPicoseconds
	ext["opcua.timestamp_origin"] = "source"
	if value.SourceTimestamp.IsZero() {
		ext["opcua.timestamp_origin"] = "server"
	}
	if origin == "gateway" {
		ext["opcua.timestamp_origin"] = "gateway"
	}
	reading := map[string]any{"value": nil, "unit": signal.Unit, "quality": "bad", "reason": fmt.Sprintf("opcua_status_0x%08x", uint32(value.StatusCode))}
	if uint32(value.StatusCode)&0x80000000 != 0 {
		return reading, stamp, origin, ext
	}
	minBound, maxBound := signal.Min, signal.Max
	if minBound == nil && maxBound == nil {
		switch signal.Name {
		case "motor_temperature":
			minBound, maxBound = floatPtr(-40), floatPtr(150)
		case "machine_state":
			minBound, maxBound = floatPtr(0), floatPtr(2)
		}
	}
	var number any
	switch signal.Type {
	case ua.DataTypeIDDouble:
		if n, ok := value.Value.(float64); ok && inBounds(n, minBound, maxBound) {
			number = n
		}
	case ua.DataTypeIDFloat:
		if n, ok := value.Value.(float32); ok && inBounds(float64(n), minBound, maxBound) {
			number = float64(n)
		}
	case ua.DataTypeIDUInt32:
		if n, ok := value.Value.(uint32); ok && inBounds(float64(n), minBound, maxBound) {
			number = n
		}
	case ua.DataTypeIDUInt16:
		if n, ok := value.Value.(uint16); ok && inBounds(float64(n), minBound, maxBound) {
			number = n
		}
	default:
		switch signal.Name {
		case "motor_temperature":
			if n, ok := value.Value.(float64); ok && inBounds(n, minBound, maxBound) {
				number = n
			}
		case "cycle_counter":
			if n, ok := value.Value.(uint32); ok && inBounds(float64(n), minBound, maxBound) {
				number = n
			}
		case "machine_state":
			if n, ok := value.Value.(uint16); ok && inBounds(float64(n), minBound, maxBound) {
				number = n
			}
		}
	}
	if number == nil {
		reading["reason"] = "opcua_type_or_range_mismatch"
		return reading, stamp, origin, ext
	}
	delete(reading, "reason")
	reading["value"] = number
	reading["quality"] = "good"
	if uint32(value.StatusCode)&0x40000000 != 0 {
		reading["quality"] = "uncertain"
	}
	return reading, stamp, origin, ext
}
