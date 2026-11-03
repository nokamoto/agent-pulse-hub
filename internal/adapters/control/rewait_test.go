package control

import "testing"

func TestRetiredRewaitRequest(t *testing.T) {
	_, err := parseRequest([]byte(`{"version":1,"op":"rewait","subscription_id":"s1","operation_id":"1700000000000:0123456789abcdef0123456789abcdef","confirmation_position":"opaque"}`))
	if err == nil || err.Code != "invalid_request" {
		t.Fatalf("retired rewait request returned %v", err)
	}
}
