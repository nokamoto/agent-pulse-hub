package control

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
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
		service:  hub.New(slog.New(slog.NewTextHandler(io.Discard, nil))),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		done:     make(chan struct{}),
		active:   make(map[net.Conn]struct{}),
	}
	defer server.Close()
	defer server.service.Shutdown()
	connection := &responseLostConn{Reader: bytes.NewReader([]byte("{\"version\":1,\"op\":\"stop\"}\n"))}
	server.handle(connection)
	select {
	case <-server.done:
	default:
		t.Fatal("server remained open after the stop response was lost")
	}
}

type responseLostConn struct{ *bytes.Reader }

func (*responseLostConn) Write([]byte) (int, error) { return 0, errors.New("response lost") }
func (*responseLostConn) Close() error              { return nil }
func (*responseLostConn) LocalAddr() net.Addr       { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func (*responseLostConn) RemoteAddr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (*responseLostConn) SetDeadline(time.Time) error      { return nil }
func (*responseLostConn) SetReadDeadline(time.Time) error  { return nil }
func (*responseLostConn) SetWriteDeadline(time.Time) error { return nil }

func TestParseRegistrationRequestValidatesExactRoutingFields(t *testing.T) {
	valid := `{"version":1,"op":"register","input_plugin":"manual","watch_args":{"n":9007199254740993},"output_plugin":"model","destination_args":{"path":"out"}}`
	for _, test := range []struct {
		name string
		wire string
		ok   bool
	}{
		{name: "valid preserves number", wire: valid, ok: true},
		{name: "wrong version token", wire: strings.Replace(valid, `"version":1`, `"version":1.0`, 1)},
		{name: "unknown field", wire: strings.TrimSuffix(valid, "}") + `,"extra":true}`},
		{name: "duplicate key", wire: strings.Replace(valid, `"input_plugin":"manual"`, `"input_plugin":"manual","input_plugin":"other"`, 1)},
		{name: "missing output", wire: `{"version":1,"op":"register","input_plugin":"manual","watch_args":{},"destination_args":{}}`},
		{name: "empty input", wire: strings.Replace(valid, `"input_plugin":"manual"`, `"input_plugin":""`, 1)},
		{name: "array watch arguments", wire: strings.Replace(valid, `"watch_args":{"n":9007199254740993}`, `"watch_args":[]`, 1)},
		{name: "null destination arguments", wire: strings.Replace(valid, `"destination_args":{"path":"out"}`, `"destination_args":null`, 1)},
		{name: "non-object", wire: `[]`},
		{name: "wrong operation", wire: `{"version":1,"op":"stop"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, parseErr := ParseRegistrationRequest([]byte(test.wire))
			if test.ok {
				if parseErr != nil || request.InputPlugin != "manual" || request.OutputPlugin != "model" {
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
