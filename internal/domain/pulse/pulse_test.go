package pulse

import (
	"encoding/json"
	"testing"
)

func TestCanonicalWatchArgs(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  string
	}{
		{
			name:  "sorts nested object keys and preserves array order",
			left:  `{"z":{"b":2,"a":1},"items":[1,2]}`,
			right: `{"items":[1.0,2e0],"z":{"a":1.0,"b":2.00}}`,
			want:  `{"items":[1e0,2e0],"z":{"a":1e0,"b":2e0}}`,
		},
		{
			name:  "normalizes exponent and decimal forms",
			left:  `{"n":1000}`,
			right: `{"n":1e3}`,
			want:  `{"n":1e3}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			left, err := CanonicalWatchArgs(json.RawMessage(test.left))
			if err != nil {
				t.Fatalf("CanonicalWatchArgs(left): %v", err)
			}
			right, err := CanonicalWatchArgs(json.RawMessage(test.right))
			if err != nil {
				t.Fatalf("CanonicalWatchArgs(right): %v", err)
			}
			if left != right {
				t.Fatalf("canonical forms differ:\nleft:  %s\nright: %s", left, right)
			}
			if left != test.want {
				t.Fatalf("canonical form = %s, want %s", left, test.want)
			}
		})
	}
}

func TestCanonicalWatchArgsRejectsInvalidValues(t *testing.T) {
	tests := []string{
		`null`,
		`[]`,
		`{"x":1,"x":2}`,
		`{"nested":{"x":1,"x":2}}`,
		`{"x":1} {"y":2}`,
	}
	for _, input := range tests {
		if _, err := CanonicalWatchArgs(json.RawMessage(input)); err == nil {
			t.Errorf("CanonicalWatchArgs(%s) succeeded, want error", input)
		}
	}
}

func TestCanonicalWatchArgsKeepsLargeIntegersExact(t *testing.T) {
	left, err := CanonicalWatchArgs(json.RawMessage(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalWatchArgs(json.RawMessage(`{"n":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatal("distinct large integers had the same canonical form")
	}
}

func TestValidateEvent(t *testing.T) {
	if err := ValidateEvent(Event{SubscriptionID: "sub-1", Context: "hello"}); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	if err := ValidateEvent(Event{SubscriptionID: "sub-1", Context: string(make([]byte, MaxContextBytes+1))}); err == nil {
		t.Fatal("oversized context was accepted")
	}
	if err := ValidateEvent(Event{Context: "hello"}); err == nil {
		t.Fatal("event without a subscription was accepted")
	}
}

func TestNewIDAndValidateUUID(t *testing.T) {
	value, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateUUID(value); err != nil {
		t.Fatalf("generated ID is invalid: %v", err)
	}
	if err := ValidateUUID("not-a-uuid"); err == nil {
		t.Fatal("invalid UUID was accepted")
	}
}
