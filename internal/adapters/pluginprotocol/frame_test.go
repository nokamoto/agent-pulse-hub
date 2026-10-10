package pluginprotocol

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestDecodeEventUsesUTF8ByteLimit(t *testing.T) {
	valid, err := Encode(Frame{Type: "event", SubscriptionID: "subscription-a", Context: strings.Repeat("é", MaxContextBytes/2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) == 0 {
		t.Fatal("encoded event is empty")
	}
	if _, err := Encode(Frame{Type: "event", SubscriptionID: "subscription-a", Context: strings.Repeat("é", MaxContextBytes/2+1)}); err == nil {
		t.Fatal("Encode accepted context over the UTF-8 byte limit")
	}
}

func TestDecodeRejectsUnknownFrameFieldsAndWrongDirection(t *testing.T) {
	value := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version": jsonvalue.NewNumber("1"),
		"type":    jsonvalue.NewString("ready"),
		"extra":   jsonvalue.NewBoolean(true),
	})
	if _, err := Decode(value, PluginToDaemon); err == nil {
		t.Fatal("Decode accepted an unknown field")
	}
	ready, err := Encode(Frame{Type: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jsonvalue.Parse(ready[:len(ready)-1], 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(parsed, DaemonToPlugin); err == nil {
		t.Fatal("Decode accepted plugin readiness in the daemon-to-plugin direction")
	}
}

func TestDecodeRejectsInvalidFrameSchemaVariants(t *testing.T) {
	for _, test := range []struct {
		name      string
		frame     string
		direction Direction
	}{
		{name: "non-object", frame: `[]`, direction: PluginToDaemon},
		{name: "missing version", frame: `{"type":"ready"}`, direction: PluginToDaemon},
		{name: "unsupported version", frame: `{"version":2,"type":"ready"}`, direction: PluginToDaemon},
		{name: "wrong version type", frame: `{"version":"1","type":"ready"}`, direction: PluginToDaemon},
		{name: "missing type", frame: `{"version":1}`, direction: PluginToDaemon},
		{name: "null type", frame: `{"version":1,"type":null}`, direction: PluginToDaemon},
		{name: "empty type", frame: `{"version":1,"type":""}`, direction: PluginToDaemon},
		{name: "ready extra field", frame: `{"version":1,"type":"ready","extra":true}`, direction: PluginToDaemon},
		{name: "watch result missing request id", frame: `{"version":1,"type":"watch_result","accepted":true}`, direction: PluginToDaemon},
		{name: "watch result null accepted", frame: `{"version":1,"type":"watch_result","request_id":"r1","accepted":null}`, direction: PluginToDaemon},
		{name: "accepted with error", frame: `{"version":1,"type":"watch_result","request_id":"r1","accepted":true,"error":"unexpected"}`, direction: PluginToDaemon},
		{name: "rejected without error", frame: `{"version":1,"type":"watch_result","request_id":"r1","accepted":false}`, direction: PluginToDaemon},
		{name: "rejected with empty error", frame: `{"version":1,"type":"watch_result","request_id":"r1","accepted":false,"error":""}`, direction: PluginToDaemon},
		{name: "event missing subscription", frame: `{"version":1,"type":"event","context":"event"}`, direction: PluginToDaemon},
		{name: "event null context", frame: `{"version":1,"type":"event","subscription_id":"s1","context":null}`, direction: PluginToDaemon},
		{name: "event empty context", frame: `{"version":1,"type":"event","subscription_id":"s1","context":""}`, direction: PluginToDaemon},
		{name: "watch missing arguments", frame: `{"version":1,"type":"watch","request_id":"r1","subscription_id":"s1"}`, direction: DaemonToPlugin},
		{name: "watch array arguments", frame: `{"version":1,"type":"watch","request_id":"r1","subscription_id":"s1","watch_args":[]}`, direction: DaemonToPlugin},
		{name: "shutdown extra field", frame: `{"version":1,"type":"shutdown","extra":true}`, direction: DaemonToPlugin},
		{name: "watch in plugin direction", frame: `{"version":1,"type":"watch","request_id":"r1","subscription_id":"s1","watch_args":{}}`, direction: PluginToDaemon},
		{name: "event in daemon direction", frame: `{"version":1,"type":"event","subscription_id":"s1","context":"event"}`, direction: DaemonToPlugin},
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

func TestDecodeAcceptsExactContextLimitAndRejectsOverflow(t *testing.T) {
	for _, size := range []int{MaxContextBytes, MaxContextBytes + 1} {
		frame := jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"version":         jsonvalue.NewNumber("1"),
			"type":            jsonvalue.NewString("event"),
			"subscription_id": jsonvalue.NewString("subscription-a"),
			"context":         jsonvalue.NewString(strings.Repeat("x", size)),
		})
		_, err := Decode(frame, PluginToDaemon)
		if size == MaxContextBytes && err != nil {
			t.Fatalf("Decode rejected context at exact byte limit: %v", err)
		}
		if size == MaxContextBytes+1 && err == nil {
			t.Fatal("Decode accepted context over the byte limit")
		}
	}
}
