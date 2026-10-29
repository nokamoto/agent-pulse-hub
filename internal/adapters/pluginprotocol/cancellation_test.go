package pluginprotocol

import (
	"strings"
	"testing"
)

func TestCancellationCapabilityComposesWithValidInputProfiles(t *testing.T) {
	for _, capabilities := range [][]string{
		{CancelWatchCapability},
		{RuntimeStateCapability, CancelWatchCapability},
		{CancelWatchCapability, RuntimeStateV2Capability},
		{RewaitCapability, CancelWatchCapability, RuntimeStateV2Capability},
	} {
		if _, err := Encode(Frame{Type: "ready", Role: "input", Capabilities: capabilities}); err != nil {
			t.Fatalf("capabilities %v: %v", capabilities, err)
		}
	}
	for _, frame := range []Frame{
		{Type: "ready", Role: "output", Capabilities: []string{CancelWatchCapability}},
		{Type: "ready", Role: "input", Capabilities: []string{CancelWatchCapability, CancelWatchCapability}},
		{Type: "ready", Role: "input", Capabilities: []string{CancelWatchCapability, RewaitCapability}},
	} {
		if _, err := Encode(frame); err == nil {
			t.Fatalf("invalid capability profile encoded: %+v", frame)
		}
	}
}

func TestCancellationSchemasAreStrictAndDirectional(t *testing.T) {
	for _, test := range []struct {
		payload   string
		direction Direction
	}{
		{`{"version":2,"type":"cancel_watch","request_id":"r","subscription_id":"s"}`, DaemonToPlugin},
		{`{"version":2,"type":"cancel_result","request_id":"r","accepted":true}`, PluginToDaemon},
		{`{"version":2,"type":"cancel_result","request_id":"r","accepted":false,"error":"busy"}`, PluginToDaemon},
	} {
		frame, err := ParsePayload([]byte(test.payload), test.direction)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Encode(frame); err != nil {
			t.Fatal(err)
		}
		other := PluginToDaemon
		if test.direction == other {
			other = DaemonToPlugin
		}
		if _, err := ParsePayload([]byte(test.payload), other); err == nil {
			t.Fatal("opposite direction was accepted")
		}
	}
	for _, payload := range []string{
		`{"version":2,"type":"cancel_watch","request_id":"r","subscription_id":"s","extra":1}`,
		`{"version":2,"type":"cancel_watch","request_id":"","subscription_id":"s"}`,
		`{"version":2,"type":"cancel_watch","request_id":"r","subscription_id":1}`,
		`{"version":2,"type":"cancel_result","request_id":"r","accepted":true,"error":"unexpected"}`,
		`{"version":2,"type":"cancel_result","request_id":"r","accepted":false}`,
		`{"version":2,"type":"cancel_result","request_id":"r","accepted":true,"subscription_id":"s"}`,
		`{"version":2,"type":"cancel_result","request_id":"r","accepted":"true"}`,
		`{"version":2,"type":"cancel_result","request_id":"r","accepted":false,"error":"` + strings.Repeat("x", 1025) + `"}`,
	} {
		direction := PluginToDaemon
		if strings.Contains(payload, `"cancel_watch"`) {
			direction = DaemonToPlugin
		}
		if _, err := ParsePayload([]byte(payload), direction); err == nil {
			t.Fatalf("malformed payload accepted: %s", payload)
		}
	}
}
