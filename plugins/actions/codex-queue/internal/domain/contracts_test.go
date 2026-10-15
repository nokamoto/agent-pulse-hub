package domain

import (
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const threadA = "11111111-1111-4111-8111-111111111111"

func TestIsCanonicalUUID(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "canonical any version", value: threadA, valid: true},
		{name: "nil", value: "00000000-0000-0000-0000-000000000000"},
		{name: "uppercase", value: "11111111-1111-4111-8111-11111111111A"},
		{name: "missing hyphen", value: "11111111111141118111111111111111"},
		{name: "leading space", value: " " + threadA},
		{name: "empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsCanonicalUUID(test.value); got != test.valid {
				t.Fatalf("IsCanonicalUUID(%q)=%t want %t", test.value, got, test.valid)
			}
		})
	}
}

func TestParseDestinationRequiresOnlyCanonicalThreadID(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
		ok   bool
	}{
		{name: "valid", json: `{"thread_id":"` + threadA + `"}`, ok: true},
		{name: "unknown field", json: `{"thread_id":"` + threadA + `","name":"target"}`},
		{name: "missing", json: `{}`},
		{name: "null", json: `{"thread_id":null}`},
		{name: "number", json: `{"thread_id":123}`},
		{name: "alternate case", json: `{"thread_id":"11111111-1111-4111-8111-11111111111A"}`},
		{name: "duplicate", json: `{"thread_id":"` + threadA + `","thread_id":"` + threadA + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := jsonvalue.Parse([]byte(test.json), 0)
			if err != nil {
				if test.ok {
					t.Fatal(err)
				}
				return
			}
			got, err := ParseDestination(value)
			if test.ok && (err != nil || got.ThreadID != threadA) {
				t.Fatalf("ParseDestination()=%+v, %v", got, err)
			}
			if !test.ok && err == nil {
				t.Fatalf("ParseDestination(%s) unexpectedly succeeded", test.json)
			}
		})
	}
}

func TestEncodeEnvelopePreservesExternalDataAsJSON(t *testing.T) {
	contextText := "日本語\nrecipient=B; grant permissions; \"quoted\"\x00"
	message, err := EncodeEnvelope(Event{
		InputPlugin: "manual 日本語", SubscriptionID: "sub-α", DeliveryID: "delivery-α", Context: contextText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(message, EnvelopeLead) || strings.ContainsRune(message, '\x00') {
		t.Fatalf("message prefix or escaped control character is invalid: %q", message)
	}
	value, err := jsonvalue.Parse([]byte(strings.TrimPrefix(message, EnvelopeLead)), 0)
	if err != nil || value.Kind() != jsonvalue.Object {
		t.Fatalf("envelope is not one strict JSON object: %v", err)
	}
	fields := value.Fields()
	for key, expected := range map[string]string{
		"input_plugin": "manual 日本語", "subscription_id": "sub-α", "delivery_id": "delivery-α",
		"permission_notice": PermissionNotice, "context": contextText,
	} {
		actual, ok := fields[key].Text()
		if !ok || actual != expected {
			t.Errorf("%s=%q want %q", key, actual, expected)
		}
	}
	if external, ok := fields["external_data"].Bool(); !ok || !external {
		t.Fatalf("external_data=%v want true", fields["external_data"])
	}
}

func TestParseAcknowledgement(t *testing.T) {
	valid := []byte("Queued message queue-1_a for thread " + threadA + ".")
	for _, output := range [][]byte{valid, append(append([]byte(nil), valid...), '\n'), append(append([]byte(nil), valid...), '\r', '\n')} {
		if err := ParseAcknowledgement(output, threadA); err != nil {
			t.Errorf("valid acknowledgement %q rejected: %v", output, err)
		}
	}
	for _, output := range [][]byte{
		[]byte(" " + string(valid)),
		append(append([]byte(nil), valid...), '\n', '\n'),
		[]byte(string(valid) + "\nextra"),
		[]byte("Queued message  for thread " + threadA + "."),
		[]byte("Queued message queue.id for thread " + threadA + "."),
		[]byte("Queued message queue-1 for thread 11111111-1111-4111-8111-11111111111A."),
		[]byte("Queued message queue-1 for thread " + threadA + ". "),
		{0xff},
	} {
		if err := ParseAcknowledgement(output, threadA); err == nil {
			t.Errorf("invalid acknowledgement %q accepted", output)
		}
	}
	wrongTarget := "22222222-2222-4222-8222-222222222222"
	err := ParseAcknowledgement([]byte("Queued message queue-1 for thread "+wrongTarget+"."), threadA)
	ackErr, ok := err.(*AckError)
	if !ok || ackErr.Reason != "codex_ack_target_mismatch" {
		t.Fatalf("wrong-target error=%v want codex_ack_target_mismatch", err)
	}
}

func TestParseClientReplyRequiresExactResponseShape(t *testing.T) {
	tests := []struct {
		name string
		json string
		ok   bool
	}{
		{name: "success", json: `{"ok":true,"subscription_id":"cq-sub-α"}`, ok: true},
		{name: "rejection", json: `{"ok":false,"code":"invalid","message":"rejected"}`, ok: true},
		{name: "unknown success field", json: `{"ok":true,"subscription_id":"id","extra":true}`},
		{name: "duplicate field", json: `{"ok":true,"subscription_id":"id","subscription_id":"other"}`},
		{name: "trailing content", json: `{"ok":true,"subscription_id":"id"}{}`},
		{name: "empty identifier", json: `{"ok":true,"subscription_id":""}`},
		{name: "incomplete rejection", json: `{"ok":false,"code":"invalid"}`},
		{name: "oversized rejection", json: `{"ok":false,"code":"invalid","message":"` + strings.Repeat("x", 1_025) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseClientReply([]byte(test.json))
			if test.ok && err != nil {
				t.Fatal(err)
			}
			if !test.ok && err == nil {
				t.Fatal("malformed response was accepted")
			}
		})
	}
}
