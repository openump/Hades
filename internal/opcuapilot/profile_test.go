package opcuapilot

import (
	"github.com/awcullen/opcua/ua"
	"testing"
	"time"
)

func TestMapStatusAndTimestampWithoutFabrication(t *testing.T) {
	source := time.Date(2026, 10, 15, 10, 0, 0, 123000000, time.UTC)
	server := source.Add(time.Second)
	for _, tc := range []struct {
		status   ua.StatusCode
		quality  string
		hasValue bool
	}{
		{ua.Good, "good", true}, {ua.UncertainLastUsableValue, "uncertain", true}, {ua.BadNodeIDUnknown, "bad", false},
	} {
		value := ua.NewDataValue(float64(67.2), tc.status, source, 0, server, 0)
		reading, stamp, origin, ext := MapValue(Signals[0], value, server.Add(time.Second))
		if reading["quality"] != tc.quality || (reading["value"] != nil) != tc.hasValue || stamp != source || origin != "device" {
			t.Fatal(reading, stamp, origin)
		}
		if ext["opcua.status_code"] != uint32(tc.status) || ext["opcua.source_timestamp"] != source.Format(time.RFC3339Nano) {
			t.Fatal(ext)
		}
	}
	value := ua.NewDataValue("wrong-type", ua.Good, time.Time{}, 0, server, 0)
	r, stamp, _, _ := MapValue(Signals[0], value, source)
	if r["value"] != nil || r["quality"] != "bad" || stamp != server {
		t.Fatal(r, stamp)
	}
	value = ua.NewDataValue(float64(67.2), ua.Good, time.Time{}, 0, time.Time{}, 0)
	_, stamp, origin, _ := MapValue(Signals[0], value, source)
	if stamp != source || origin != "gateway" {
		t.Fatal(stamp, origin)
	}
}
func TestEndpointScope(t *testing.T) {
	for _, endpoint := range []string{"opc.tcp://192.0.2.1:4840", "opc.tcp://localhost:4840", "http://127.0.0.1:4840", "opc.tcp://user@127.0.0.1:4840"} {
		if ValidateEndpoint(endpoint) == nil {
			t.Fatal(endpoint)
		}
	}
	if err := ValidateEndpoint("opc.tcp://127.0.0.1:4840"); err != nil {
		t.Fatal(err)
	}
}
