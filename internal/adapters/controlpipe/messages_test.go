package controlpipe

import (
	"strings"
	"testing"
)

func TestParseRegistrationRequestDistinguishesIdentityAndShapeErrors(t *testing.T) {
	missingIdentity := []byte(`{"version":1,"op":"register","plugin":"manual","watch_args":{}}`)
	if _, err := ParseRegistrationRequest(missingIdentity); err == nil || err.Code != "missing_session" {
		t.Fatalf("ParseRegistrationRequest() error = %#v, want missing_session", err)
	}
	wrongArguments := []byte(`{"version":1,"op":"register","plugin":"manual","session_id":"11111111-1111-4111-8111-111111111111","watch_args":[]}`)
	if _, err := ParseRegistrationRequest(wrongArguments); err == nil || err.Code != "invalid_request" {
		t.Fatalf("ParseRegistrationRequest() error = %#v, want invalid_request", err)
	}
}

func TestRegistrationResponseHasExactFieldsAndBoundedMessage(t *testing.T) {
	for _, response := range []string{
		`{"ok":true,"subscription_id":"subscription-a","extra":true}`,
		`{"ok":false,"code":"unknown_plugin","message":"no","extra":true}`,
	} {
		if _, err := ParseRegistrationResponse([]byte(response)); err == nil {
			t.Errorf("ParseRegistrationResponse(%s) succeeded", response)
		}
	}

	wire, err := EncodeError("invalid_request", strings.Repeat("x", MaxErrorMessageBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	response, err := ParseRegistrationResponse(wire[:len(wire)-1])
	if err != nil {
		t.Fatal(err)
	}
	if len([]byte(response.Message)) != MaxErrorMessageBytes {
		t.Fatalf("message length = %d, want %d", len([]byte(response.Message)), MaxErrorMessageBytes)
	}
}
