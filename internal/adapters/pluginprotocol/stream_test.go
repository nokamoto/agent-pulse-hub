package pluginprotocol

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestStreamCapabilityComposition(t *testing.T) {
	for _, test := range []struct {
		caps  []string
		valid bool
	}{
		{[]string{RuntimeStateV2Capability, RuntimeStateStreamCapability}, true},
		{[]string{RuntimeStateStreamCapability, RewaitCapability, CancelWatchCapability, RuntimeStateV2Capability}, true},
		{[]string{RuntimeStateStreamCapability}, false},
		{[]string{RuntimeStateCapability, RuntimeStateStreamCapability}, false},
		{[]string{RuntimeStateV2Capability, RuntimeStateStreamCapability, RuntimeStateStreamCapability}, false},
		{[]string{RuntimeStateV2Capability, RuntimeStateStreamCapability, "unknown"}, false},
	} {
		_, err := Encode(Frame{Type: "ready", Role: "input", Capabilities: test.caps})
		if (err == nil) != test.valid {
			t.Errorf("caps %v = %v", test.caps, err)
		}
	}
}

func TestStreamFramesStrictBoundsAndCounters(t *testing.T) {
	object := jsonvalue.NewObject(nil)
	for _, frame := range []Frame{
		{Type: "state_stream_watch", RequestID: "r", SubscriptionID: "s", WatchArgs: object, ChunkCount: "0"},
		{Type: "state_stream_watch", RequestID: "r", SubscriptionID: "s", WatchArgs: object, Schema: "schema", ChunkCount: "2"},
		{Type: "state_stream_chunk", RequestID: "r", SubscriptionID: "s", Index: "18446744073709551615", Data: object},
		{Type: "state_stream_end", RequestID: "r", SubscriptionID: "s", ChunkCount: "0"},
		{Type: "checkpoint_stream_begin", RequestID: "r", SubscriptionID: "s", Schema: "schema"},
		{Type: "checkpoint_stream_chunk", RequestID: "r", SubscriptionID: "s", Index: "0", Data: object},
		{Type: "checkpoint_stream_end", RequestID: "r", SubscriptionID: "s", ChunkCount: "1"},
	} {
		encoded, err := Encode(frame)
		if err != nil {
			t.Fatal(frame.Type, err)
		}
		if _, err := ParsePayload(encoded, directionFor(frame.Type)); err != nil {
			t.Fatal(err)
		}
		extra := strings.TrimSuffix(string(encoded), "}") + `,"unknown":true}`
		if _, err := ParsePayload([]byte(extra), directionFor(frame.Type)); err == nil {
			t.Fatal("unknown stream field accepted")
		}
	}
	for _, counter := range []string{"", "00", "01", "+1", "-1", "1.0", "1e1", "18446744073709551616"} {
		if _, err := StreamCounter(counter, false); err == nil {
			t.Errorf("counter %q accepted", counter)
		}
	}
	if _, err := Encode(Frame{Type: "state_stream_watch", RequestID: "r", SubscriptionID: "s", WatchArgs: object, Schema: "schema", ChunkCount: "0"}); err == nil {
		t.Fatal("nonnull zero count accepted")
	}
	for _, size := range []int{32768, 32769} {
		data := jsonvalue.NewObject(map[string]*jsonvalue.Value{"x": jsonvalue.NewString(strings.Repeat("x", size-8))})
		_, err := Encode(Frame{Type: "checkpoint_stream_chunk", RequestID: "r", SubscriptionID: "s", Index: "0", Data: data})
		if (err == nil) != (size == 32768) {
			t.Errorf("object size %d err=%v", size, err)
		}
	}
}
