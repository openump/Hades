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
}

var Signals = [...]Signal{
	{"motor_temperature", "Temperature", "Cel", ua.DataTypeIDDouble},
	{"cycle_counter", "CycleCounter", "1", ua.DataTypeIDUInt32},
	{"machine_state", "MachineState", "1", ua.DataTypeIDUInt16},
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
	reading := map[string]any{"value": nil, "unit": signal.Unit, "quality": "bad", "reason": fmt.Sprintf("opcua_status_0x%08x", uint32(value.StatusCode))}
	if uint32(value.StatusCode)&0x80000000 != 0 {
		return reading, stamp, origin, ext
	}
	var number any
	switch signal.Name {
	case "motor_temperature":
		if n, ok := value.Value.(float64); ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= -40 && n <= 150 {
			number = n
		}
	case "cycle_counter":
		if n, ok := value.Value.(uint32); ok {
			number = n
		}
	case "machine_state":
		if n, ok := value.Value.(uint16); ok && n <= 2 {
			number = n
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
