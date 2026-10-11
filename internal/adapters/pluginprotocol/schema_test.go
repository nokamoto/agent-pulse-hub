package pluginprotocol

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestEveryFrameRequiredFieldRejectsMissingNullAndWrongType(t *testing.T) {
	for _, test := range []struct {
		name, input string
		direction   Direction
	}{
		{name: "ready", input: `{"version":1,"type":"ready"}`, direction: PluginToDaemon},
		{name: "accepted", input: `{"version":1,"type":"watch_result","request_id":"opaque request","accepted":true}`, direction: PluginToDaemon},
		{name: "rejected", input: `{"version":1,"type":"watch_result","request_id":"opaque request","accepted":false,"error":"reject"}`, direction: PluginToDaemon},
		{name: "event", input: `{"version":1,"type":"event","subscription_id":"opaque subscription","context":"line\nnext"}`, direction: PluginToDaemon},
		{name: "watch", input: `{"version":1,"type":"watch","request_id":"opaque request","subscription_id":"opaque subscription","watch_args":{"n":9007199254740993}}`, direction: DaemonToPlugin},
		{name: "shutdown", input: `{"version":1,"type":"shutdown"}`, direction: DaemonToPlugin},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, err := jsonvalue.Parse([]byte(test.input), 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(base, test.direction); err != nil {
				t.Fatalf("valid frame rejected: %v", err)
			}
			for field, old := range base.Fields() {
				for _, mutation := range []string{"missing", "null", "wrong type"} {
					t.Run(field+"/"+mutation, func(t *testing.T) {
						fields := base.Fields()
						switch mutation {
						case "missing":
							delete(fields, field)
						case "null":
							fields[field] = jsonvalue.NewNull()
						default:
							if old.Kind() == jsonvalue.String {
								fields[field] = jsonvalue.NewBoolean(true)
							} else {
								fields[field] = jsonvalue.NewString("wrong")
							}
						}
						if _, err := Decode(jsonvalue.NewObject(fields), test.direction); err == nil {
							t.Fatal("invalid field accepted")
						}
					})
				}
			}
			fields := base.Fields()
			fields["unknown"] = jsonvalue.NewBoolean(true)
			if _, err := Decode(jsonvalue.NewObject(fields), test.direction); err == nil {
				t.Fatal("unknown field accepted")
			}
			other := PluginToDaemon
			if test.direction == other {
				other = DaemonToPlugin
			}
			if _, err := Decode(base, other); err == nil {
				t.Fatal("wrong direction accepted")
			}
		})
	}
}

func TestVersionRequiresExactNumericToken(t *testing.T) {
	for _, version := range []string{"1.0", "1e0", "2", "-1", "null", `"1"`} {
		value, err := jsonvalue.Parse([]byte(`{"version":`+version+`,"type":"ready"}`), 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(value, PluginToDaemon); err == nil {
			t.Errorf("accepted version %s", version)
		}
	}
}

func TestWatchFrameExactWireBound(t *testing.T) {
	base := Frame{Type: "watch", RequestID: "opaque", SubscriptionID: "opaque", WatchArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{"data": jsonvalue.NewString("")})}
	frame, err := Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{protocol.MaxFrameBytes, protocol.MaxFrameBytes + 1} {
		base.WatchArgs = jsonvalue.NewObject(map[string]*jsonvalue.Value{"data": jsonvalue.NewString(strings.Repeat("x", size-len(frame)))})
		got, err := Encode(base)
		if size == protocol.MaxFrameBytes {
			if err != nil || len(got) != size {
				t.Fatalf("exact bound len %d err %v", len(got), err)
			}
		} else if err == nil {
			t.Fatal("over-limit watch encoded")
		}
	}
}

func TestEventContextBoundAfterEscapeDecoding(t *testing.T) {
	for _, size := range []int{MaxContextBytes, MaxContextBytes + 1} {
		value, err := jsonvalue.Parse([]byte(`{"version":1,"type":"event","subscription_id":"opaque","context":"`+strings.Repeat(`\u0061`, size)+`"}`), 0)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := Decode(value, PluginToDaemon)
		if size == MaxContextBytes {
			if err != nil || frame.Context != strings.Repeat("a", size) {
				t.Fatalf("context size %d error %v", len(frame.Context), err)
			}
		} else if err == nil {
			t.Fatal("escaped oversized context accepted")
		}
	}
}
