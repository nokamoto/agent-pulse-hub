package protocol

import (
	"bufio"
	"errors"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

func TestReadFrameEnforcesLimitsAndNewline(t *testing.T) {
	tests := []struct {
		name string
		data string
		max  int
		want error
	}{
		{name: "valid", data: "{}\n", max: 3},
		{name: "too large", data: "{}\n", max: 2, want: ErrFrameTooLarge},
		{name: "unterminated", data: "{}", max: 3, want: ErrUnterminatedFrame},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadFrame(bufio.NewReader(strings.NewReader(test.data)), test.max)
			if test.want == nil && err != nil {
				t.Fatalf("ReadFrame() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("ReadFrame() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodePluginFrameRejectsUnknownAndDuplicateFields(t *testing.T) {
	tests := []string{
		`{"version":1,"type":"ready","extra":true}`,
		`{"version":1,"version":1,"type":"ready"}`,
		`{"version":1,"type":"event","subscription_id":"sub","context":"x","context":"y"}`,
		`{"version":2,"type":"ready"}`,
	}
	for _, data := range tests {
		if _, err := DecodePluginFrame([]byte(data)); err == nil {
			t.Errorf("DecodePluginFrame(%s) succeeded", data)
		}
	}
}

func TestDecodeControlRequestRequiresUUIDAndObject(t *testing.T) {
	for _, data := range []string{
		`{"version":1,"op":"register","plugin":"manual","session_id":"unknown","watch_args":{}}`,
		`{"version":1,"op":"register","plugin":"manual","session_id":"01a11c69-4d5f-7bf1-9506-c872a3543cf4","watch_args":[]}`,
		`{"version":1,"op":"register","plugin":"manual","session_id":"01a11c69-4d5f-7bf1-9506-c872a3543cf4","watch_args":{"x":1,"x":2}}`,
	} {
		if _, err := DecodeControlRequest([]byte(data)); err == nil {
			t.Errorf("DecodeControlRequest(%s) succeeded", data)
		}
	}
}

func TestDecodeControlResponseRequiresDefiniteSuccess(t *testing.T) {
	valid := `{"version":1,"ok":true,"subscription_id":"01a11c69-4d5f-7bf1-9506-c872a3543cf4"}`
	if response, err := DecodeControlResponse([]byte(valid)); err != nil || !response.OK {
		t.Fatalf("DecodeControlResponse(%s) = %+v, %v", valid, response, err)
	}
	for _, invalid := range []string{
		`{"version":1,"ok":true}`,
		`{"version":1,"ok":true,"subscription_id":"sub-1"}`,
		`{"version":1,"ok":true,"subscription_id":"01a11c69-4d5f-7bf1-9506-c872a3543cf4","error":""}`,
		`{"version":1,"ok":false,"code":"rejected","message":"no","subscription_id":""}`,
	} {
		if _, err := DecodeControlResponse([]byte(invalid)); err == nil {
			t.Errorf("malformed control response was accepted: %s", invalid)
		}
	}
}

func TestDecodeWatchResultEnforcesErrorSemantics(t *testing.T) {
	for _, data := range []string{
		`{"version":1,"type":"watch_result","request_id":"r1","accepted":true,"error":""}`,
		`{"version":1,"type":"watch_result","request_id":"r1","accepted":false}`,
		`{"version":1,"type":"watch_result","request_id":"r1","accepted":false,"error":""}`,
	} {
		if _, err := DecodePluginFrame([]byte(data)); err == nil {
			t.Errorf("malformed watch result was accepted: %s", data)
		}
	}
}

func TestDecodePluginEventHonorsContextLimit(t *testing.T) {
	large := strings.Repeat("x", pulse.MaxContextBytes+1)
	encoded := `{"version":1,"type":"event","subscription_id":"sub-1","context":"` + large + `"}`
	if _, err := DecodePluginFrame([]byte(encoded)); err == nil {
		t.Fatal("oversized context was accepted")
	}
}
