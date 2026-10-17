package pluginprotocol

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestRuntimeStateFramesRoundTrip(t *testing.T) {
	accepted := true
	frames := []struct {
		frame     Frame
		direction Direction
	}{
		{Frame{Type: "ready", Role: "input", Capabilities: []string{RuntimeStateCapability}}, PluginToDaemon},
		{Frame{Type: "ready", Role: "input", Capabilities: []string{}}, PluginToDaemon},
		{Frame{Type: "state_watch", RequestID: "w1", SubscriptionID: "s1", WatchArgs: jsonvalue.NewObject(nil), State: jsonvalue.NewNull()}, DaemonToPlugin},
		{Frame{Type: "state_watch", RequestID: "w2", SubscriptionID: "s1", WatchArgs: jsonvalue.NewObject(nil), State: testRuntimeState()}, DaemonToPlugin},
		{Frame{Type: "state_event", SubscriptionID: "s1", EventID: "18446744073709551615", Context: "U1"}, PluginToDaemon},
		{Frame{Type: "delivery_notice", SubscriptionID: "s1", EventID: "1", Status: "discarded"}, DaemonToPlugin},
		{Frame{Type: "checkpoint", RequestID: "c1", SubscriptionID: "s1", Quiesce: true}, DaemonToPlugin},
		{Frame{Type: "checkpoint_result", RequestID: "c1", Accepted: &accepted, State: testRuntimeState()}, PluginToDaemon},
		{Frame{Type: "checkpoint_result", RequestID: "c2", Accepted: boolValue(false), Error: "state unavailable"}, PluginToDaemon},
	}
	for _, test := range frames {
		payload, err := Encode(test.frame)
		if err != nil {
			t.Errorf("Encode(%s): %v", test.frame.Type, err)
			continue
		}
		if _, err := ParsePayload(payload, test.direction); err != nil {
			t.Errorf("ParsePayload(%s): %v", test.frame.Type, err)
		}
	}
}

func TestRuntimeStateFramesRejectInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		direction     Direction
	}{
		{"output capability", `{"version":2,"type":"ready","role":"output","capabilities":["runtime_state_v1"]}`, PluginToDaemon},
		{"unknown capability", `{"version":2,"type":"ready","role":"input","capabilities":["unknown"]}`, PluginToDaemon},
		{"duplicate capability", `{"version":2,"type":"ready","role":"input","capabilities":["runtime_state_v1","runtime_state_v1"]}`, PluginToDaemon},
		{"non-string capability", `{"version":2,"type":"ready","role":"input","capabilities":[1]}`, PluginToDaemon},
		{"zero event id", `{"version":2,"type":"state_event","subscription_id":"s1","event_id":"0","context":"U1"}`, PluginToDaemon},
		{"leading-zero event id", `{"version":2,"type":"state_event","subscription_id":"s1","event_id":"01","context":"U1"}`, PluginToDaemon},
		{"overflow event id", `{"version":2,"type":"state_event","subscription_id":"s1","event_id":"18446744073709551616","context":"U1"}`, PluginToDaemon},
		{"empty context", `{"version":2,"type":"state_event","subscription_id":"s1","event_id":"1","context":""}`, PluginToDaemon},
		{"invalid state payload", `{"version":2,"type":"state_watch","request_id":"w1","subscription_id":"s1","watch_args":{},"state":{"schema":"v1","payload":[]}}`, DaemonToPlugin},
		{"invalid notice status", `{"version":2,"type":"delivery_notice","subscription_id":"s1","event_id":"1","status":"retry"}`, DaemonToPlugin},
		{"accepted result without state", `{"version":2,"type":"checkpoint_result","request_id":"c1","accepted":true}`, PluginToDaemon},
		{"accepted result with error", `{"version":2,"type":"checkpoint_result","request_id":"c1","accepted":true,"error":9,"state":{"schema":"v1","payload":{}}}`, PluginToDaemon},
		{"rejected result with state", `{"version":2,"type":"checkpoint_result","request_id":"c1","accepted":false,"error":"failed","state":{"schema":"v1","payload":{}}}`, PluginToDaemon},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := jsonvalue.Parse([]byte(test.payload), 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(value, test.direction); err == nil {
				t.Fatalf("accepted invalid payload: %s", test.payload)
			}
		})
	}
}

func TestRuntimeStateEnvelopeLimits(t *testing.T) {
	for _, schema := range []string{"", strings.Repeat("x", MaxRuntimeSchemaBytes+1), strings.Repeat("é", MaxRuntimeSchemaBytes/2+1)} {
		state := jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"schema": jsonvalue.NewString(schema), "payload": jsonvalue.NewObject(nil),
		})
		if err := ValidateStateEnvelope(state); err == nil {
			t.Errorf("accepted schema with %d UTF-8 bytes", len([]byte(schema)))
		}
	}
	tooLarge := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"schema": jsonvalue.NewString("v1"),
		"payload": jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"padding": jsonvalue.NewString(strings.Repeat("x", MaxRuntimeStateBytes)),
		}),
	})
	if err := ValidateStateEnvelope(tooLarge); err == nil {
		t.Fatal("accepted an oversized state envelope")
	}
}

func testRuntimeState() *jsonvalue.Value {
	return jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"schema": jsonvalue.NewString("cursor-v1"),
		"payload": jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"confirmed": jsonvalue.NewArray(jsonvalue.NewString("U1")),
		}),
	})
}

func boolValue(value bool) *bool { return &value }
