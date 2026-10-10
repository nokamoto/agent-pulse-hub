//go:build windows

package controlpipe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
	"golang.org/x/sys/windows"
)

const (
	pipeBufferSize            = 64 * 1024
	tokenAppContainerSIDClass = 31
)

func pipeName() (string, error) {
	sid, err := currentSID()
	if err != nil {
		return "", err
	}
	return `\\.\pipe\agent-pulse-hub-v1-` + sid, nil
}

func serve(ctx context.Context, logger *slog.Logger, handler Handler, onReady func()) error {
	if logger == nil {
		logger = slog.Default()
	}
	name, err := pipeName()
	if err != nil {
		return err
	}
	sid, err := currentSID()
	if err != nil {
		return err
	}
	appContainerSID, err := currentAppContainerSID()
	if err != nil {
		return err
	}
	dacl := "D:P(A;;GA;;;" + sid + ")"
	if appContainerSID != "" {
		dacl += "(A;;GA;;;" + appContainerSID + ")"
	}
	descriptor, err := windows.SecurityDescriptorFromString(dacl + "S:(ML;;NW;;;LW)")
	if err != nil {
		return fmt.Errorf("create named pipe access descriptor: %w", err)
	}
	namePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	securityAttributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	pipe, err := windows.CreateNamedPipe(
		namePointer,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_NOWAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1,
		pipeBufferSize,
		pipeBufferSize,
		0,
		&securityAttributes,
	)
	if err != nil {
		return fmt.Errorf("reserve local control pipe %s: %w", name, err)
	}
	defer windows.CloseHandle(pipe)
	logger.Info("control_pipe_ready", "pipe", name)
	if onReady != nil {
		onReady()
	}
	firstConnection := true
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if !firstConnection {
			state := uint32(windows.PIPE_NOWAIT)
			if err := windows.SetNamedPipeHandleState(pipe, &state, nil, nil); err != nil {
				return fmt.Errorf("prepare local control pipe: %w", err)
			}
		}
		if err := connectPipe(ctx, pipe); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept local control connection: %w", err)
		}
		firstConnection = false
		state := uint32(windows.PIPE_READMODE_BYTE | windows.PIPE_NOWAIT)
		if err := windows.SetNamedPipeHandleState(pipe, &state, nil, nil); err != nil {
			_ = windows.DisconnectNamedPipe(pipe)
			return fmt.Errorf("set local control pipe mode: %w", err)
		}
		if err := verifyPipeClient(pipe, sid); err != nil {
			logger.Warn("control_client_rejected", "reason", bounded(err.Error(), 256))
			_ = windows.DisconnectNamedPipe(pipe)
			continue
		}
		if err := serveOne(ctx, pipe, logger, handler); err != nil && ctx.Err() == nil {
			logger.Warn("control_request_failed", "reason", bounded(err.Error(), 256))
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := windows.DisconnectNamedPipe(pipe); err != nil && !errors.Is(err, syscall.Errno(windows.ERROR_PIPE_NOT_CONNECTED)) {
			return fmt.Errorf("disconnect local control pipe: %w", err)
		}
	}
}

func connectPipe(ctx context.Context, pipe windows.Handle) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := windows.ConnectNamedPipe(pipe, nil)
		if err == nil || errors.Is(err, syscall.Errno(windows.ERROR_PIPE_CONNECTED)) {
			return nil
		}
		if errors.Is(err, syscall.Errno(windows.ERROR_PIPE_LISTENING)) || errors.Is(err, syscall.Errno(windows.ERROR_NO_DATA)) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		return err
	}
}

func serveOne(ctx context.Context, pipe windows.Handle, logger *slog.Logger, handler Handler) error {
	requestLine, err := readPipeFrame(ctx, pipe)
	if err != nil {
		return fmt.Errorf("read control request: %w", err)
	}
	request, err := protocol.DecodeControlRequest(requestLine)
	if err != nil {
		code := "invalid_request"
		message := "request must include version 1, register operation, plugin, UUID session_id, and object watch_args"
		if strings.Contains(err.Error(), "session_id") {
			code = "missing_identity"
			message = "session_id must be provided by the current Codex conversation"
		} else if strings.Contains(err.Error(), "watch_args") {
			code = "invalid_watch_args"
			message = "watch_args must be a valid JSON object"
		}
		return writeResponse(ctx, pipe, protocol.ControlResponse{Version: protocol.Version, OK: false, Code: code, Message: message})
	}
	logger.Info("control_request_received", "plugin", request.Plugin, "session_id", request.SessionID)
	response := handler(ctx, request)
	if err := writeResponse(ctx, pipe, response); err != nil {
		if response.SubscriptionID != "" {
			logger.Warn("control_response_lost", "plugin", request.Plugin, "subscription_id", response.SubscriptionID, "outcome", "unknown")
		}
		return err
	}
	logger.Info("control_response_sent", "plugin", request.Plugin, "ok", response.OK, "subscription_id", response.SubscriptionID)
	return nil
}

func writeResponse(ctx context.Context, pipe windows.Handle, response protocol.ControlResponse) error {
	line, err := protocol.EncodeLine(response)
	if err != nil {
		return err
	}
	if err := writePipeFrame(ctx, pipe, line, nil); err != nil {
		return err
	}
	if err := windows.FlushFileBuffers(pipe); err != nil {
		return fmt.Errorf("flush control response: %w", err)
	}
	return nil
}

func register(ctx context.Context, request protocol.ControlRequest) (protocol.ControlResponse, error) {
	if request.Version != protocol.Version || request.Operation != "register" {
		return protocol.ControlResponse{}, errors.New("invalid registration request")
	}
	name, err := pipeName()
	if err != nil {
		return protocol.ControlResponse{}, err
	}
	pipeNamePointer, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return protocol.ControlResponse{}, err
	}
	pipe, err := openPipe(ctx, pipeNamePointer)
	if err != nil {
		return protocol.ControlResponse{}, fmt.Errorf("daemon unavailable: %w", err)
	}
	defer windows.CloseHandle(pipe)
	if err := verifyPipeServer(pipe); err != nil {
		return protocol.ControlResponse{}, fmt.Errorf("reject unverifiable daemon: %w", err)
	}
	state := uint32(windows.PIPE_READMODE_BYTE | windows.PIPE_NOWAIT)
	if err := windows.SetNamedPipeHandleState(pipe, &state, nil, nil); err != nil {
		return protocol.ControlResponse{}, fmt.Errorf("prepare daemon pipe: %w", err)
	}
	line, err := protocol.EncodeLine(request)
	if err != nil {
		return protocol.ControlResponse{}, err
	}
	sent := false
	if err := writePipeFrame(ctx, pipe, line, &sent); err != nil {
		if sent {
			return protocol.ControlResponse{}, uncertain(err)
		}
		return protocol.ControlResponse{}, err
	}
	data, err := readPipeFrame(ctx, pipe)
	if err != nil {
		return protocol.ControlResponse{}, uncertain(err)
	}
	response, err := protocol.DecodeControlResponse(data)
	if err != nil {
		return protocol.ControlResponse{}, uncertain(err)
	}
	return response, nil
}

func openPipe(ctx context.Context, name *uint16) (windows.Handle, error) {
	waitNamedPipe := windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")
	deadline, hasDeadline := ctx.Deadline()
	for {
		pipe, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			return pipe, nil
		}
		if !errors.Is(err, syscall.Errno(windows.ERROR_PIPE_BUSY)) && !errors.Is(err, syscall.Errno(windows.ERROR_FILE_NOT_FOUND)) {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		wait := uint32(100)
		if hasDeadline {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return 0, context.DeadlineExceeded
			}
			if remaining < 100*time.Millisecond {
				wait = uint32(remaining.Milliseconds())
				if wait == 0 {
					wait = 1
				}
			}
		}
		_, _, _ = waitNamedPipe.Call(uintptr(unsafe.Pointer(name)), uintptr(wait))
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
	}
}

func verifyPipeServer(pipe windows.Handle) error {
	var processID uint32
	if err := windows.GetNamedPipeServerProcessId(pipe, &processID); err != nil {
		return err
	}
	serverSID, err := processSID(processID)
	if err != nil {
		return err
	}
	current, err := currentSID()
	if err != nil {
		return err
	}
	if serverSID != current {
		return errors.New("daemon SID does not match the current user")
	}
	return nil
}

func verifyPipeClient(pipe windows.Handle, expectedSID string) error {
	var processID uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &processID); err != nil {
		return err
	}
	clientSID, err := processSID(processID)
	if err != nil {
		return err
	}
	if clientSID != expectedSID {
		return errors.New("client SID does not match the daemon user")
	}
	return nil
}

func currentSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current user SID: %w", err)
	}
	return user.User.Sid.String(), nil
}

type tokenAppContainerInformation struct {
	SID *windows.SID
}

func currentAppContainerSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	var required uint32
	err := windows.GetTokenInformation(token, tokenAppContainerSIDClass, nil, 0, &required)
	if !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		if err != nil {
			return "", fmt.Errorf("read current app container SID: %w", err)
		}
		return "", nil
	}
	if required < uint32(unsafe.Sizeof(tokenAppContainerInformation{})) {
		return "", errors.New("read current app container SID: token data is too short")
	}
	buffer := make([]byte, required)
	if err := windows.GetTokenInformation(token, tokenAppContainerSIDClass, &buffer[0], required, &required); err != nil {
		return "", fmt.Errorf("read current app container SID: %w", err)
	}
	information := (*tokenAppContainerInformation)(unsafe.Pointer(&buffer[0]))
	if information.SID == nil {
		return "", nil
	}
	return information.SID.String(), nil
}

func processSID(processID uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return "", fmt.Errorf("open pipe peer process: %w", err)
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", fmt.Errorf("open pipe peer token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read pipe peer SID: %w", err)
	}
	return user.User.Sid.String(), nil
}

func writePipeFrame(ctx context.Context, pipe windows.Handle, data []byte, sent *bool) error {
	for len(data) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		var written uint32
		err := windows.WriteFile(pipe, data, &written, nil)
		if written > 0 {
			if sent != nil {
				*sent = true
			}
			data = data[written:]
		}
		if err != nil && !errors.Is(err, syscall.Errno(windows.ERROR_NO_DATA)) {
			return err
		}
		if err != nil || written == 0 {
			if err := waitPipeRetry(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func readPipeFrame(ctx context.Context, pipe windows.Handle) ([]byte, error) {
	data := make([]byte, pulse.MaxFrameBytes+1)
	length := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if length == len(data) {
			return nil, protocol.ErrFrameTooLarge
		}
		var read uint32
		err := windows.ReadFile(pipe, data[length:], &read, nil)
		if read > 0 {
			start := length
			length += int(read)
			if newline := bytes.IndexByte(data[start:length], '\n'); newline >= 0 {
				end := start + newline
				if end+1 > pulse.MaxFrameBytes {
					return nil, protocol.ErrFrameTooLarge
				}
				if end+1 != length {
					return nil, fmt.Errorf("%w: multiple control frames on one connection", protocol.ErrInvalidFrame)
				}
				frame := bytes.TrimSuffix(data[:end], []byte{'\r'})
				if !utf8.Valid(frame) {
					return nil, fmt.Errorf("%w: frame is not valid UTF-8", protocol.ErrInvalidFrame)
				}
				return bytes.Clone(frame), nil
			}
			if length >= pulse.MaxFrameBytes {
				return nil, protocol.ErrFrameTooLarge
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, syscall.Errno(windows.ERROR_NO_DATA)):
				if retryErr := waitPipeRetry(ctx); retryErr != nil {
					return nil, retryErr
				}
			case errors.Is(err, syscall.Errno(windows.ERROR_BROKEN_PIPE)), errors.Is(err, syscall.Errno(windows.ERROR_PIPE_NOT_CONNECTED)):
				if length == 0 {
					return nil, io.EOF
				}
				return nil, protocol.ErrUnterminatedFrame
			default:
				return nil, err
			}
		} else if read == 0 {
			if retryErr := waitPipeRetry(ctx); retryErr != nil {
				return nil, retryErr
			}
		}
	}
}

func waitPipeRetry(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func uncertain(err error) error {
	return fmt.Errorf("registration outcome unknown; repeat the same request to recover its result: %w", err)
}

func bounded(value string, maximum int) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\t' || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}
