package control

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
)

func TestUnsubscribeStrictRequestMatrix(t *testing.T) {
	id := "8fc5db2a-77a0-4f60-bf92-bbe214a83a71"
	valid := `{"version":1,"op":"unsubscribe","subscription_id":"` + id + `"}`
	request, err := parseRequest([]byte(valid))
	if err != nil || request.operation != "unsubscribe" || request.subscriptionID != id {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	for _, wire := range []string{
		`{"version":1,"op":"unsubscribe"}`,
		strings.Replace(valid, `"`+id+`"`, `null`, 1),
		strings.Replace(valid, `"`+id+`"`, `4`, 1),
		strings.Replace(valid, id, strings.ToUpper(id), 1),
		strings.Replace(valid, id, "", 1),
		strings.Replace(valid, id, "../../registrations/target", 1),
		strings.TrimSuffix(valid, "}") + `,"extra":true}`,
		strings.TrimSuffix(valid, "}") + `,"subscription_id":"` + id + `"}`,
		strings.Replace(valid, `"version":1`, `"version":1.0`, 1),
	} {
		if _, err := parseRequest([]byte(wire)); err == nil {
			t.Fatalf("accepted invalid request %s", wire)
		}
	}
}

func TestUnsubscribeClientChecksMatchingResponseAndDoesNotRetry(t *testing.T) {
	id := "8fc5db2a-77a0-4f60-bf92-bbe214a83a71"
	for _, test := range []struct {
		name, wire string
		uncertain  bool
		rejected   bool
	}{
		{name: "success", wire: `{"ok":true,"subscription_id":"` + id + `"}`},
		{name: "wrong ID", wire: `{"ok":true,"subscription_id":"other"}`, uncertain: true},
		{name: "malformed", wire: `{"ok":true,"subscription_id":"` + id + `","extra":1}`, uncertain: true},
		{name: "lost response", uncertain: true},
		{name: "not found", wire: `{"ok":false,"code":"unknown_subscription","message":"No registration."}`, rejected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			request := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					request <- ""
					return
				}
				defer conn.Close()
				frame, _ := protocol.ReadFrame(bufio.NewReader(conn), protocol.MaxFrameBytes)
				request <- string(frame)
				if test.wire != "" {
					_, _ = conn.Write([]byte(test.wire + "\n"))
				}
			}()
			response, _, err := Unsubscribe(context.Background(), listener.Addr().String(), id)
			var uncertain *UncertainError
			var rejected *DaemonError
			if errors.As(err, &uncertain) != test.uncertain || errors.As(err, &rejected) != test.rejected || (err == nil) != (!test.uncertain && !test.rejected) {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			parsed, parseErr := parseRequest([]byte(<-request))
			if parseErr != nil || parsed.subscriptionID != id || parsed.operation != "unsubscribe" {
				t.Fatalf("request=%+v error=%v", parsed, parseErr)
			}
		})
	}
}
