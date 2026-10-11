package control

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestValidateAddressRequiresLiteralIPv4LoopbackAndValidPort(t *testing.T) {
	for _, address := range []string{DefaultAddress, "127.2.3.4:1", "127.0.0.1:65535"} {
		if err := validateAddress(address); err != nil {
			t.Errorf("validateAddress(%q): %v", address, err)
		}
	}
	for _, address := range []string{"localhost:47831", "0.0.0.0:47831", "192.0.2.1:47831", "[::1]:47831", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:+1", "127.0.0.1: 1"} {
		if err := validateAddress(address); err == nil {
			t.Errorf("validateAddress(%q) succeeded", address)
		}
	}
}

func TestStopClosesServerWhenAcknowledgementIsLost(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		listener: listener,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		done:     make(chan struct{}),
		active:   make(map[net.Conn]struct{}),
	}
	defer server.Close()
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		defer serverConnection.Close()
		server.handle(serverConnection)
	}()
	if _, err := clientConnection.Write([]byte("{\"version\":1,\"op\":\"stop\"}\n")); err != nil {
		t.Fatal(err)
	}
	if err := clientConnection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("stop handler did not finish after response loss")
	}
	select {
	case <-server.done:
	default:
		t.Fatal("server remained open after the stop response was lost")
	}
}

func TestParseRegistrationRequestValidatesExactFieldsAndIdentity(t *testing.T) {
	const validSession = "00000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name string
		wire string
		ok   bool
	}{
		{name: "valid preserves number", wire: `{"version":1,"op":"register","plugin":"manual","session_id":"` + validSession + `","watch_args":{"n":9007199254740993}}`, ok: true},
		{name: "wrong version token", wire: `{"version":1.0,"op":"register","plugin":"manual","session_id":"` + validSession + `","watch_args":{}}`},
		{name: "unknown field", wire: `{"version":1,"op":"register","plugin":"manual","session_id":"` + validSession + `","watch_args":{},"extra":true}`},
		{name: "duplicate key", wire: `{"version":1,"op":"register","plugin":"manual","plugin":"other","session_id":"` + validSession + `","watch_args":{}}`},
		{name: "missing session", wire: `{"version":1,"op":"register","plugin":"manual","watch_args":{}}`},
		{name: "invalid session", wire: `{"version":1,"op":"register","plugin":"manual","session_id":"not-a-uuid","watch_args":{}}`},
		{name: "empty plugin", wire: `{"version":1,"op":"register","plugin":"","session_id":"` + validSession + `","watch_args":{}}`},
		{name: "array arguments", wire: `{"version":1,"op":"register","plugin":"manual","session_id":"` + validSession + `","watch_args":[]}`},
		{name: "null arguments", wire: `{"version":1,"op":"register","plugin":"manual","session_id":"` + validSession + `","watch_args":null}`},
		{name: "non-object", wire: `[]`},
		{name: "wrong operation", wire: `{"version":1,"op":"stop"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, parseErr := ParseRegistrationRequest([]byte(test.wire))
			if test.ok {
				if parseErr != nil || request.Plugin != "manual" || request.SessionID != validSession {
					t.Fatalf("ParseRegistrationRequest() = %#v, %v", request, parseErr)
				}
				number, _ := request.WatchArgs.Get("n")
				if token, ok := number.Text(); !ok || token != "9007199254740993" {
					t.Fatalf("exact number token = %q, %v", token, ok)
				}
			} else if parseErr == nil || parseErr.Code == "" {
				t.Fatal("invalid registration accepted without a coded rejection")
			}
		})
	}
}

func TestParseRegistrationResponseRequiresExactSuccessOrErrorShape(t *testing.T) {
	for _, test := range []struct {
		name string
		wire string
		ok   bool
	}{
		{name: "success", wire: `{"ok":true,"subscription_id":"s1"}`, ok: true},
		{name: "error", wire: `{"ok":false,"code":"invalid_request","message":"rejected"}`, ok: true},
		{name: "success extra field", wire: `{"ok":true,"subscription_id":"s1","extra":1}`},
		{name: "empty success id", wire: `{"ok":true,"subscription_id":""}`},
		{name: "error empty code", wire: `{"ok":false,"code":"","message":"rejected"}`},
		{name: "error empty message", wire: `{"ok":false,"code":"invalid_request","message":""}`},
		{name: "error oversized message", wire: `{"ok":false,"code":"x","message":"` + strings.Repeat("x", MaxErrorMessageBytes+1) + `"}`},
		{name: "unknown field", wire: `{"ok":false,"code":"x","message":"y","extra":true}`},
		{name: "duplicate field", wire: `{"ok":true,"ok":true,"subscription_id":"s1"}`},
		{name: "non-object", wire: `[]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := ParseRegistrationResponse([]byte(test.wire))
			if test.ok {
				if err != nil {
					t.Fatal(err)
				}
				if test.name == "success" && (!response.Success || response.SubscriptionID != "s1") {
					t.Fatalf("success response = %#v", response)
				}
				if test.name == "error" && (response.Success || response.Code != "invalid_request" || response.Message != "rejected") {
					t.Fatalf("error response = %#v", response)
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestParseStopResponseRequiresEmptySuccessShape(t *testing.T) {
	if response, err := parseStopResponse([]byte(`{"ok":true}`)); err != nil || !response.Success {
		t.Fatalf("valid stop response = %#v, %v", response, err)
	}
	response, err := parseStopResponse([]byte(`{"ok":false,"code":"stopping","message":"already stopping"}`))
	if err != nil || response.Success || response.Code != "stopping" {
		t.Fatalf("valid stop rejection = %#v, %v", response, err)
	}
	for _, wire := range []string{`{"ok":true,"extra":1}`, `{"ok":false,"code":"","message":"x"}`, `{"ok":false}`} {
		if _, err := parseStopResponse([]byte(wire)); err == nil {
			t.Errorf("invalid stop response %s accepted", wire)
		}
	}
}

func TestEncodeErrorBoundsUTF8AndRejectsEmptyCode(t *testing.T) {
	if _, err := EncodeError("", "message"); err == nil {
		t.Fatal("empty code accepted")
	}
	wire, err := EncodeError("failure", strings.Repeat("あ", MaxErrorMessageBytes))
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonvalue.Parse(wire, 0)
	if err != nil {
		t.Fatal(err)
	}
	messageValue, _ := value.Get("message")
	message, valid := jsonvalue.ParseString(messageValue)
	if !valid || len(message) > MaxErrorMessageBytes {
		t.Fatalf("error message bound invalid: bytes=%d valid=%v", len(message), valid)
	}
}
