package jsonstrict

import "testing"

func TestCheckRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	for _, value := range []string{`{"x":1,"x":2}`, `{"outer":{"x":1,"x":2}}`} {
		if err := Check([]byte(value)); err == nil {
			t.Errorf("Check(%s) succeeded, want duplicate-key error", value)
		}
	}
}

func TestCheckRejectsTrailingValues(t *testing.T) {
	if err := Check([]byte(`{} []`)); err == nil {
		t.Fatal("trailing JSON value was accepted")
	}
}
