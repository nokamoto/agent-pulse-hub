package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
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
		return "control result is uncertain"
	}
	return "control result is uncertain: " + e.Reason
}

func Register(ctx context.Context, address, inputPlugin string, watchArgs *jsonvalue.Value, outputPlugin string, destinationArgs *jsonvalue.Value) (RegistrationResponse, []byte, error) {
	if inputPlugin == "" || outputPlugin == "" || watchArgs == nil || watchArgs.Kind() != jsonvalue.Object || destinationArgs == nil || destinationArgs.Kind() != jsonvalue.Object {
		return RegistrationResponse{}, nil, errors.New("registration inputs are invalid")
	}
	request := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version":          jsonvalue.NewNumber("1"),
		"op":               jsonvalue.NewString("register"),
		"input_plugin":     jsonvalue.NewString(inputPlugin),
		"watch_args":       watchArgs,
		"output_plugin":    jsonvalue.NewString(outputPlugin),
		"destination_args": destinationArgs,
	})
	return exchange(ctx, address, request, ParseRegistrationResponse)
}

func Stop(ctx context.Context, address string) error {
	request := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version": jsonvalue.NewNumber("1"),
		"op":      jsonvalue.NewString("stop"),
	})
	_, _, err := exchange(ctx, address, request, parseStopResponse)
	return err
}

func exchange(ctx context.Context, address string, request *jsonvalue.Value, parse func([]byte) (RegistrationResponse, error)) (RegistrationResponse, []byte, error) {
	if err := validateAddress(address); err != nil {
		return RegistrationResponse{}, nil, err
	}
	requestBytes, err := protocol.EncodeFrame(request)
	if err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("encode control request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("connect to daemon: %w", err)
	}
	defer connection.Close()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("set control deadline: %w", err)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if err := ctx.Err(); err != nil {
		return RegistrationResponse{}, nil, fmt.Errorf("control request canceled before send: %w", err)
	}
	written, err := writeRequest(connection, requestBytes)
	if err != nil {
		if written != 0 {
			return RegistrationResponse{}, nil, &UncertainError{Reason: "the request may have reached the daemon"}
		}
		return RegistrationResponse{}, nil, fmt.Errorf("send control request: %w", err)
	}
	frame, err := protocol.ReadFrame(bufio.NewReader(connection), protocol.MaxFrameBytes)
	if err != nil {
		return RegistrationResponse{}, nil, &UncertainError{Reason: "no valid daemon response was received"}
	}
	response, err := parse(frame)
	if err != nil {
		return RegistrationResponse{}, nil, &UncertainError{Reason: "the daemon response was invalid"}
	}
	wire := append(append([]byte(nil), frame...), '\n')
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
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
