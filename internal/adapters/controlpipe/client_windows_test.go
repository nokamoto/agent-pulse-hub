//go:build windows

package controlpipe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/windowsidentity"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestRegisterContextDeadlineCoversResponseRead(t *testing.T) {
	sid, err := windowsidentity.CurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	pipeName := fmt.Sprintf("%sclient-timeout-%d", controlPipePrefix, time.Now().UnixNano())
	listener, err := winio.ListenPipe(pipeName, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	releaseServer := make(chan struct{})
	defer close(releaseServer)
	requestRead := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			requestRead <- acceptErr
			return
		}
		defer connection.Close()
		_, readErr := protocol.ReadFrame(bufio.NewReader(connection), protocol.MaxFrameBytes)
		requestRead <- readErr
		if readErr == nil {
			<-releaseServer
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, registerErr := register(
			ctx,
			"manual",
			"123e4567-e89b-12d3-a456-426614174000",
			jsonvalue.NewObject(nil),
			pipeName,
			sid,
		)
		result <- registerErr
	}()

	select {
	case readErr := <-requestRead:
		if readErr != nil {
			t.Fatalf("server could not read registration request: %v", readErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not send a registration request")
	}

	select {
	case registerErr := <-result:
		var uncertain *UncertainError
		if !errors.As(registerErr, &uncertain) {
			t.Fatalf("Register() error = %v, want uncertain response result", registerErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Register() did not stop waiting when its context deadline expired")
	}
}
