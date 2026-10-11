//go:build windows

package controlpipe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type DaemonError struct {
	Response RegistrationResponse
	Wire     []byte
}

func (e *DaemonError) Error() string { return e.Response.Message }

type UncertainError struct{ Reason string }

func (e *UncertainError) Error() string {
	if e.Reason == "" {
		return "registration result is uncertain"
	}
	return "registration result is uncertain: " + e.Reason
}

func Register(ctx context.Context, pluginName, sessionID string, watchArgs *jsonvalue.Value) (RegistrationResponse, []byte, error) {
	if pluginName == "" || watchArgs == nil || watchArgs.Kind() != jsonvalue.Object {
		return RegistrationResponse{}, nil, errors.New("registration inputs are invalid")
	}
	if !domain.IsUUID(sessionID) {
		return RegistrationResponse{}, nil, errors.New("session identity must be a UUID")
	}
	currentSID, err := (windowsPipes{}).CurrentUserSID()
	if err != nil {
		return RegistrationResponse{}, nil, err
	}
	return register(ctx, pluginName, sessionID, watchArgs, PipeName(currentSID), currentSID)
}

func register(ctx context.Context, pluginName, sessionID string, watchArgs *jsonvalue.Value, pipeName, currentSID string) (RegistrationResponse, []byte, error) {
	return registerWith(ctx, pluginName, sessionID, watchArgs, pipeName, currentSID, windowsPipes{})
}

func registerWith(ctx context.Context, pluginName, sessionID string, watchArgs *jsonvalue.Value, pipeName, currentSID string, pipes pipeSystem) (RegistrationResponse, []byte, error) {
	request := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":    jsonvalue.NewNumber("1"),
		"op":         jsonvalue.NewString("register"),
		"plugin":     jsonvalue.NewString(pluginName),
		"session_id": jsonvalue.NewString(sessionID),
		"watch_args": watchArgs,
	})
	requestBytes, err := protocol.EncodeFrame(request)
	if err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("encode registration request: %w", err)
	}
	connection, err := pipes.Dial(ctx, pipeName)
	if err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return RegistrationResponse{}, nil, fmt.Errorf("set registration deadline: %w", err)
		}
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if err := ctx.Err(); err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("registration canceled before request: %w", err)
	}

	serverSID, err := pipes.ServerUserSID(connection)
	if err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("verify daemon user identity: %w", err)
	}
	if !strings.EqualFold(serverSID, currentSID) {
		return RegistrationResponse{}, nil, errors.New("daemon pipe server belongs to a different user")
	}
	written, writeErr := writeRequest(connection, requestBytes)
	if writeErr != nil {
		if written > 0 {
			return RegistrationResponse{}, nil, &UncertainError{Reason: "the request may have reached the daemon"}
		}
		return RegistrationResponse{}, nil, fmt.Errorf("send registration request: %w", writeErr)
	}
	responseFrame, err := protocol.ReadFrame(bufio.NewReader(connection), protocol.MaxFrameBytes)
	if err != nil {
		return RegistrationResponse{}, nil, &UncertainError{Reason: "no valid daemon response was received"}
	}
	response, err := ParseRegistrationResponse(responseFrame)
	if err != nil {
		return RegistrationResponse{}, nil, &UncertainError{Reason: "the daemon response was invalid"}
	}
	wire := append(append([]byte(nil), responseFrame...), '\n')
	if !response.Success {
		return response, wire, &DaemonError{Response: response, Wire: wire}
	}
	return response, wire, nil
}

func writeRequest(writer io.Writer, data []byte) (int, error) {
	total := 0
	for total < len(data) {
		written, err := writer.Write(data[total:])
		total += written
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, errors.New("short pipe write")
		}
	}
	return total, nil
}
