package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/control"
)

type rewaitStub struct {
	commandOperationsStub
	position      string
	positionErr   error
	positionReads int
	rewaitCalls   int
	rewaitArgs    []string
}

func (s *rewaitStub) Position(string) (string, error) {
	s.positionReads++
	return s.position, s.positionErr
}

func (s *rewaitStub) Rewait(_ context.Context, address, id, operation, position string) (control.RegistrationResponse, []byte, error) {
	s.rewaitCalls++
	s.rewaitArgs = []string{address, id, operation, position}
	return s.response, s.responseWire, s.registerErr
}

func TestRewaitCommandDoesNotRetryOrReregister(t *testing.T) {
	op := "1700000000000:0123456789abcdef0123456789abcdef"
	args := []string{"rewait", "--subscription-id", "s1", "--operation-id", op, "--confirmation-position-file", "position.json"}
	for _, test := range []struct {
		name string
		err  error
		wire string
		code int
	}{
		{name: "success", wire: `{"ok":true,"subscription_id":"s1","operation_id":"` + op + `"}` + "\n"},
		{name: "uncertain", err: &control.UncertainError{Reason: "lost"}, code: 1},
		{name: "rejected", err: &control.DaemonError{Wire: []byte("error-wire\n")}, wire: "error-wire\n", code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &rewaitStub{position: "opaque"}
			stub.registerErr = test.err
			stub.responseWire = []byte(test.wire)
			var stdout, stderr strings.Builder
			code := runWith(args, &stdout, &stderr, stub)
			if code != test.code || stdout.String() != test.wire || stub.rewaitCalls != 1 || stub.registerCall != 0 || stub.positionReads != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q calls=%d register=%d", code, stdout.String(), stderr.String(), stub.rewaitCalls, stub.registerCall)
			}
			if stub.rewaitArgs[1] != "s1" || stub.rewaitArgs[2] != op || stub.rewaitArgs[3] != "opaque" {
				t.Fatalf("rewait args=%v", stub.rewaitArgs)
			}
		})
	}
	stub := &rewaitStub{positionErr: errors.New("invalid position file")}
	var stdout, stderr strings.Builder
	if runWith(args, &stdout, &stderr, stub) != 1 || stub.rewaitCalls != 0 {
		t.Fatal("invalid file reached daemon")
	}
	args[4] = "bad"
	stub = &rewaitStub{position: "opaque"}
	stdout.Reset()
	stderr.Reset()
	if runWith(args, &stdout, &stderr, stub) != 1 || stub.positionReads != 0 || stub.rewaitCalls != 0 {
		t.Fatal("invalid operation reached file/daemon")
	}
}
