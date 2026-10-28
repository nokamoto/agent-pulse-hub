package pluginprotocol

import (
	"strings"
	"testing"
)

func TestNotificationProfileStrictResultMatrix(t *testing.T) {
	base := `{"version":2,"type":"notification_result","request_id":"11111111-1111-4111-8111-111111111111","delivery_id":"22222222-2222-4222-8222-222222222222",`
	for _, test := range []struct {
		name, tail string
		valid      bool
	}{
		{"accepted", `"status":"accepted","receipt":"opaque"}`, true},
		{"unknown", `"status":"unknown","reason":"queue_unknown","message":"No confirmation."}`, true},
		{"unsubmitted", `"status":"unsubmitted","reason":"resources","message":"Could not start.","retryable":true,"retry_after_ms":60000}`, true},
		{"permanent", `"status":"unsubmitted","reason":"missing","message":"Missing executable.","retryable":false}`, true},
		{"accepted_failure", `"status":"accepted","reason":"bad"}`, false},
		{"unknown_retry", `"status":"unknown","reason":"unknown","message":"Unknown.","retryable":true}`, false},
		{"rejected_retry", `"status":"rejected","reason":"rejected","message":"Rejected.","retryable":false}`, false},
		{"missing_proof", `"status":"unsubmitted","reason":"missing","message":"Missing."}`, false},
		{"guidance_false", `"status":"unsubmitted","reason":"missing","message":"Missing.","retryable":false,"retry_after_ms":1}`, false},
		{"fraction", `"status":"unsubmitted","reason":"missing","message":"Missing.","retryable":true,"retry_after_ms":1.0}`, false},
		{"receipt_unknown", `"status":"unknown","reason":"unknown","message":"Unknown.","receipt":"false"}`, false},
		{"bad_reason", `"status":"unknown","reason":"Raw Service Text","message":"Unknown."}`, false},
		{"duplicate", `"status":"accepted","status":"unknown"}`, false},
		{"long_message", `"status":"unknown","reason":"unknown","message":"` + strings.Repeat("x", 1025) + `"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, err := ParsePayload([]byte(base+test.tail), PluginToDaemon)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
			if err == nil {
				encoded, err := Encode(f)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := ParsePayload(encoded, PluginToDaemon); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNotificationCapabilityAndSupplement(t *testing.T) {
	for _, test := range []struct {
		frame string
		valid bool
	}{
		{`{"version":2,"type":"ready","role":"output","capabilities":["notification_delivery_v1"]}`, true},
		{`{"version":2,"type":"ready","role":"output","capabilities":[]}`, true},
		{`{"version":2,"type":"ready","role":"input","capabilities":["notification_delivery_v1"]}`, false},
		{`{"version":2,"type":"ready","role":"output","capabilities":["runtime_state_v1"]}`, false},
		{`{"version":2,"type":"ready","role":"output","capabilities":["notification_delivery_v1","notification_delivery_v1"]}`, false},
		{`{"version":2,"type":"notification_evidence","request_id":"11111111-1111-4111-8111-111111111111","delivery_id":"22222222-2222-4222-8222-222222222222","receipt":"late"}`, true},
		{`{"version":2,"type":"notification_evidence","request_id":"11111111-1111-4111-8111-111111111111","delivery_id":"22222222-2222-4222-8222-222222222222","status":"accepted"}`, false},
	} {
		_, err := ParsePayload([]byte(test.frame), PluginToDaemon)
		if (err == nil) != test.valid {
			t.Fatalf("frame=%s valid=%v err=%v", test.frame, test.valid, err)
		}
	}
}
