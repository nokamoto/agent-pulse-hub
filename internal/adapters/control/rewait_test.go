package control

import (
	"strings"
	"testing"
)

const testOperation = "1700000000000:0123456789abcdef0123456789abcdef"

func TestRewaitStrictRequestAndResponse(t *testing.T) {
	valid := `{"version":1,"op":"rewait","subscription_id":"s1","operation_id":"` + testOperation + `","confirmation_position":"opaque"}`
	request, err := parseRequest([]byte(valid))
	if err != nil || request.operation != "rewait" || request.subscriptionID != "s1" || request.operationID != testOperation || request.confirmationPosition != "opaque" {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"opaque"`, `null`, 1),
		strings.Replace(valid, `"opaque"`, `""`, 1),
		strings.Replace(valid, `"opaque"`, `"`+strings.Repeat("日", 1366)+`"`, 1),
		strings.Replace(valid, `"subscription_id":"s1"`, `"subscription_id":""`, 1),
		strings.Replace(valid, `"op":"rewait"`, `"op":"rewait","destination_args":{}`, 1),
		strings.Replace(valid, testOperation, "01:0123456789abcdef0123456789abcdef", 1),
	} {
		if _, err := parseRequest([]byte(invalid)); err == nil {
			t.Fatal("invalid rewait accepted")
		}
	}
	wire, encodeErr := EncodeRewaitSuccess("s1", testOperation)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	response, parseErr := parseRewaitResponse(wire[:len(wire)-1])
	if parseErr != nil || !response.Success || response.SubscriptionID != "s1" || response.OperationID != testOperation {
		t.Fatalf("response=%+v error=%v", response, parseErr)
	}
	for _, invalid := range []string{`{"ok":true,"subscription_id":"s1"}`, `{"ok":true,"subscription_id":"s1","operation_id":"bad"}`, `{"ok":true,"subscription_id":"s1","operation_id":"` + testOperation + `","extra":true}`} {
		if _, err := parseRewaitResponse([]byte(invalid)); err == nil {
			t.Fatal("invalid rewait response accepted")
		}
	}
}

func TestRewaitOperationSyntaxAndPositionBytes(t *testing.T) {
	for _, id := range []string{testOperation, "0:00000000000000000000000000000000"} {
		if !ValidOperationID(id) {
			t.Fatalf("valid operation rejected: %s", id)
		}
	}
	for _, id := range []string{"", "1", "+1:0123456789abcdef0123456789abcdef", "-1:0123456789abcdef0123456789abcdef", "01:0123456789abcdef0123456789abcdef", "9223372036854775808:0123456789abcdef0123456789abcdef", "1:0123456789ABCDEF0123456789abcdef", "1:0123456789abcdef0123456789abcdef0"} {
		if ValidOperationID(id) {
			t.Fatalf("invalid operation accepted: %s", id)
		}
	}
	if !ValidPosition(strings.Repeat("x", 4096)) || ValidPosition(strings.Repeat("x", 4097)) || ValidPosition(string([]byte{0xff})) || ValidPosition("") {
		t.Fatal("position UTF-8/size validation changed")
	}
}
