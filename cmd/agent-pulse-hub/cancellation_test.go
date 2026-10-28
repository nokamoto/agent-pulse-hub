package main

import (
	"context"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/control"
)

type cancellationStub struct {
	commandOperationsStub
	calls       int
	address, id string
}

func (s *cancellationStub) Unsubscribe(_ context.Context, address, id string) (control.RegistrationResponse, []byte, error) {
	s.calls++
	s.address, s.id = address, id
	return s.response, s.responseWire, s.registerErr
}

func TestUnsubscribeRequiresOneExplicitCanonicalIDBeforeCallingDaemon(t *testing.T) {
	id := "8fc5db2a-77a0-4f60-bf92-bbe214a83a71"
	for _, args := range [][]string{
		{"unsubscribe"},
		{"unsubscribe", "--subscription-id", ""},
		{"unsubscribe", "--subscription-id", strings.ToUpper(id)},
		{"unsubscribe", "--subscription-id", id, "--subscription-id", id},
		{"unsubscribe", "--subscription-id", id, "extra"},
		{"unsubscribe", "--subscription-id", id, "--watch-args-file", "args.json"},
		{"unsubscribe", "--subscription-id", "not-a-uuid"},
	} {
		stub := &cancellationStub{}
		var stdout, stderr strings.Builder
		if runWith(args, &stdout, &stderr, stub) != 1 || stub.calls != 0 || stub.registerCall != 0 || stdout.Len() != 0 {
			t.Fatalf("args=%v calls=%d stdout=%q stderr=%q", args, stub.calls, stdout.String(), stderr.String())
		}
	}
}

func TestUnsubscribeReportsOnceWithoutRegistrationOrAutomaticRetry(t *testing.T) {
	id := "8fc5db2a-77a0-4f60-bf92-bbe214a83a71"
	for _, test := range []struct {
		name string
		err  error
		wire string
		code int
	}{
		{name: "success", wire: `{"ok":true,"subscription_id":"` + id + `"}` + "\n"},
		{name: "uncertain", err: &control.UncertainError{Reason: "lost response"}, code: 1},
		{name: "unknown", err: &control.DaemonError{Wire: []byte("rejected\n")}, wire: "rejected\n", code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &cancellationStub{}
			stub.responseWire, stub.registerErr = []byte(test.wire), test.err
			var stdout, stderr strings.Builder
			code := runWith([]string{"unsubscribe", "--subscription-id", id, "--address", "127.0.0.1:12345"}, &stdout, &stderr, stub)
			if code != test.code || stdout.String() != test.wire || stub.calls != 1 || stub.registerCall != 0 || stub.id != id || stub.address != "127.0.0.1:12345" {
				t.Fatalf("code=%d stdout=%q stderr=%q calls=%d id=%q", code, stdout.String(), stderr.String(), stub.calls, stub.id)
			}
		})
	}
}
