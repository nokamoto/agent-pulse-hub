package pluginprotocol

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestRewaitProfileCapabilitiesAndFrames(t *testing.T) {
	for _, capabilities := range [][]string{{RuntimeStateV2Capability, RewaitCapability}, {RewaitCapability, RuntimeStateV2Capability}} {
		if _, err := Encode(Frame{Type: "ready", Role: "input", Capabilities: capabilities}); err != nil {
			t.Fatal(err)
		}
	}
	for _, capabilities := range [][]string{{RewaitCapability}, {RewaitCapability, RuntimeStateCapability}, {RuntimeStateV2Capability, RewaitCapability, RewaitCapability}, {RuntimeStateCapability, RuntimeStateV2Capability, RewaitCapability}} {
		if _, err := Encode(Frame{Type: "ready", Role: "input", Capabilities: capabilities}); err == nil {
			t.Fatalf("accepted invalid capabilities %v", capabilities)
		}
	}
	for _, frame := range []Frame{
		{Type: "activate_watch", SubscriptionID: "s", Generation: "g"},
		{Type: "state_event", SubscriptionID: "s", Generation: "g", EventID: "1", Context: "external"},
		{Type: "resume_watch", RequestID: "r", SubscriptionID: "s", Generation: "g", ConfirmationPosition: strings.Repeat("a", 4096)},
		{Type: "resume_result", RequestID: "r", Accepted: boolValue(true)},
		{Type: "resume_result", RequestID: "r", Accepted: boolValue(false), Error: strings.Repeat("a", 1024)},
	} {
		encoded, err := Encode(frame)
		if err != nil {
			t.Fatalf("%s: %v", frame.Type, err)
		}
		decoded, err := ParsePayload(encoded, directionFor(frame.Type))
		if err != nil || decoded.Generation != frame.Generation || decoded.ConfirmationPosition != frame.ConfirmationPosition {
			t.Fatalf("roundtrip %s: %+v %v", frame.Type, decoded, err)
		}
	}
	for _, raw := range []string{
		`{"version":2,"type":"resume_watch","request_id":"r","subscription_id":"s","generation":"g","confirmation_position":""}`,
		`{"version":2,"type":"resume_watch","request_id":"r","subscription_id":"s","confirmation_position":"p"}`,
		`{"version":2,"type":"activate_watch","subscription_id":"s","generation":null}`,
		`{"version":2,"type":"state_event","subscription_id":"s","event_id":"1","context":"c","generation":""}`,
		`{"version":2,"type":"resume_result","request_id":"r","accepted":true,"error":"bad"}`,
		`{"version":2,"type":"resume_result","request_id":"r","accepted":false}`,
	} {
		value, err := jsonvalue.Parse([]byte(raw), 0)
		if err != nil {
			t.Fatal(err)
		}
		kind, _ := value.Get("type")
		name, _ := jsonvalue.ParseString(kind)
		if _, err := Decode(value, directionFor(name)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := Encode(Frame{Type: "resume_watch", RequestID: "r", SubscriptionID: "s", Generation: "g", ConfirmationPosition: strings.Repeat("a", 4097)}); err == nil {
		t.Fatal("oversized position accepted")
	}
	if _, err := Encode(Frame{Type: "resume_result", RequestID: "r", Accepted: boolValue(false), Error: strings.Repeat("a", 1025)}); err == nil {
		t.Fatal("oversized error accepted")
	}
}
