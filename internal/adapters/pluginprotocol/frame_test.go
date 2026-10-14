package pluginprotocol

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestReadyRequiresKnownRole(t *testing.T) {
	for _, role := range []string{"input", "output"} {
		payload, err := Encode(Frame{Type: "ready", Role: role})
		if err != nil {
			t.Fatalf("encode %s ready: %v", role, err)
		}
		frame, err := ParsePayload(payload, PluginToDaemon)
		if err != nil || frame.Role != role {
			t.Fatalf("parse %s ready = %+v, %v", role, frame, err)
		}
	}
	if _, err := Encode(Frame{Type: "ready"}); err == nil {
		t.Fatal("ready without role encoded")
	}
}

func TestRoundTripsV2RoleMessages(t *testing.T) {
	accepted := true
	falseValue := false
	frames := []Frame{
		{Type: "ready", Role: "input"},
		{Type: "watch_result", RequestID: "w1", Accepted: &accepted},
		{Type: "destination_result", RequestID: "d1", Accepted: &falseValue, Error: "invalid destination"},
		{Type: "event", SubscriptionID: "s1", Context: "PULSE-context"},
		{Type: "watch", RequestID: "w1", SubscriptionID: "s1", WatchArgs: jsonvalue.NewObject(nil)},
		{Type: "validate_destination", RequestID: "d1", DestinationArgs: jsonvalue.NewObject(nil)},
		{Type: "deliver", RequestID: "r1", DeliveryID: "d1", InputPlugin: "input-a", SubscriptionID: "s1", DestinationArgs: jsonvalue.NewObject(nil), Context: "PULSE-context", ExternalData: &accepted, PermissionNotice: PermissionNotice},
		{Type: "delivery_result", RequestID: "r1", Status: "accepted"},
		{Type: "delivery_result", RequestID: "r2", Status: "failed", Error: "disk full"},
		{Type: "delivery_result", RequestID: "r3", Status: "unknown", Error: "partial write"},
	}
	for _, frame := range frames {
		payload, err := Encode(frame)
		if err != nil {
			t.Errorf("Encode(%s): %v", frame.Type, err)
			continue
		}
		direction := PluginToDaemon
		if frame.Type == "watch" || frame.Type == "validate_destination" || frame.Type == "deliver" {
			direction = DaemonToPlugin
		}
		if _, err := ParsePayload(payload, direction); err != nil {
			t.Errorf("ParsePayload(%s): %v", frame.Type, err)
		}
	}
}

func TestDecodeRejectsMalformedSchemaDirectionAndVersion(t *testing.T) {
	for _, test := range []struct {
		name      string
		frame     string
		direction Direction
	}{
		{name: "non object", frame: `[]`, direction: PluginToDaemon},
		{name: "missing version", frame: `{"type":"ready","role":"input"}`, direction: PluginToDaemon},
		{name: "old version", frame: `{"version":1,"type":"ready","role":"input"}`, direction: PluginToDaemon},
		{name: "wrong version token", frame: `{"version":2.0,"type":"ready","role":"input"}`, direction: PluginToDaemon},
		{name: "ready missing role", frame: `{"version":2,"type":"ready"}`, direction: PluginToDaemon},
		{name: "ready unknown role", frame: `{"version":2,"type":"ready","role":"daemon"}`, direction: PluginToDaemon},
		{name: "ready extra field", frame: `{"version":2,"type":"ready","role":"input","extra":true}`, direction: PluginToDaemon},
		{name: "rejection without error", frame: `{"version":2,"type":"watch_result","request_id":"r1","accepted":false}`, direction: PluginToDaemon},
		{name: "acceptance with error", frame: `{"version":2,"type":"destination_result","request_id":"r1","accepted":true,"error":"unexpected"}`, direction: PluginToDaemon},
		{name: "delivery error missing", frame: `{"version":2,"type":"delivery_result","request_id":"r1","status":"unknown"}`, direction: PluginToDaemon},
		{name: "wrong status", frame: `{"version":2,"type":"delivery_result","request_id":"r1","status":"retry"}`, direction: PluginToDaemon},
		{name: "output request in plugin direction", frame: `{"version":2,"type":"deliver"}`, direction: PluginToDaemon},
		{name: "event in daemon direction", frame: `{"version":2,"type":"event","subscription_id":"s1","context":"x"}`, direction: DaemonToPlugin},
		{name: "unknown field", frame: `{"version":2,"type":"ready","role":"input","extra":true}`, direction: PluginToDaemon},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := jsonvalue.Parse([]byte(test.frame), 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(value, test.direction); err == nil {
				t.Fatalf("Decode(%s) succeeded", test.frame)
			}
		})
	}
}

func TestContextLimitUsesUTF8BytesAndCompleteDeliveryBound(t *testing.T) {
	accepted := true
	for _, length := range []int{MaxContextBytes, MaxContextBytes + 1} {
		_, err := Encode(Frame{Type: "event", SubscriptionID: "s1", Context: strings.Repeat("é", length/2) + strings.Repeat("x", length%2)})
		if length == MaxContextBytes && err != nil {
			t.Fatalf("exact UTF-8 byte limit rejected: %v", err)
		}
		if length == MaxContextBytes+1 && err == nil {
			t.Fatal("context over UTF-8 byte limit accepted")
		}
	}
	tooLarge := jsonvalue.NewObject(map[string]*jsonvalue.Value{"blob": jsonvalue.NewString(strings.Repeat("x", MaxPayloadBytes))})
	_, err := Encode(Frame{Type: "deliver", RequestID: "r1", DeliveryID: "d1", InputPlugin: "p", SubscriptionID: "s1", DestinationArgs: tooLarge, Context: "event", ExternalData: &accepted, PermissionNotice: PermissionNotice})
	if err == nil {
		t.Fatal("oversized complete delivery encoded")
	}
}
