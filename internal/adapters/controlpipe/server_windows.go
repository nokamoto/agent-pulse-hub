//go:build windows

package controlpipe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
)

const controlPipePrefix = `\\.\pipe\agent-pulse-hub-v1-`

type Server struct {
	listener net.Listener
	service  registrar
	logger   *slog.Logger

	mu     sync.Mutex
	closed bool
	active map[net.Conn]struct{}
}

func NewServer(service *hub.Hub, logger *slog.Logger) (*Server, error) {
	return newServer(service, logger, windowsPipes{})
}

func newServer(service registrar, logger *slog.Logger, pipes pipeSystem) (*Server, error) {
	sid, err := pipes.CurrentUserSID()
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	name := PipeName(sid)
	descriptor := "D:P(A;;GA;;;" + sid + ")"
	listener, err := pipes.Listen(name, descriptor)
	if err != nil {
		return nil, fmt.Errorf("reserve user control pipe: %w", err)
	}
	return &Server{listener: listener, service: service, logger: logger, active: make(map[net.Conn]struct{})}, nil
}

func PipeName(sid string) string { return controlPipePrefix + sid }

func (s *Server) Serve(ctx context.Context) error {
	var clients sync.WaitGroup
	go func() {
		<-ctx.Done()
		s.closeConnections()
	}()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			s.closeConnections()
			return fmt.Errorf("accept local control connection: %w", err)
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = connection.Close()
			continue
		}
		s.active[connection] = struct{}{}
		s.mu.Unlock()
		clients.Add(1)
		go func() {
			defer clients.Done()
			defer s.removeConnection(connection)
			s.handle(connection)
		}()
	}
	s.closeConnections()
	if ctx.Err() != nil {
		return nil
	}
	clients.Wait()
	return nil
}

func (s *Server) Close() error {
	s.closeConnections()
	return nil
}

func (s *Server) closeConnections() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	connections := make([]net.Conn, 0, len(s.active))
	for connection := range s.active {
		connections = append(connections, connection)
	}
	s.mu.Unlock()
	_ = s.listener.Close()
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (s *Server) removeConnection(connection net.Conn) {
	s.mu.Lock()
	delete(s.active, connection)
	s.mu.Unlock()
	_ = connection.Close()
}

func (s *Server) handle(connection net.Conn) {
	_ = connection.SetDeadline(time.Now().Add(15 * time.Second))
	frame, err := protocol.ReadFrame(bufio.NewReader(connection), protocol.MaxFrameBytes)
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			s.writeError(connection, "resource_exhausted", "Control request exceeds the frame limit.")
		} else if !errors.Is(err, io.EOF) {
			s.writeError(connection, "invalid_json", "Control request frame is incomplete.")
		}
		return
	}
	request, requestErr := ParseRegistrationRequest(frame)
	if requestErr != nil {
		s.writeHubError(connection, requestErr)
		return
	}
	subscriptionID, registerErr := s.service.Register(request.Plugin, request.SessionID, request.WatchArgs)
	if registerErr != nil {
		s.writeHubError(connection, registerErr)
		return
	}
	response, err := EncodeSuccess(string(subscriptionID))
	if err != nil {
		s.logger.Error("control_response_error", "reason", safeText(err.Error(), 256))
		return
	}
	if err := writeAll(connection, response); err != nil {
		s.logger.Warn("control_response_lost", "plugin", request.Plugin, "subscription_id", subscriptionID, "reason", safeText(err.Error(), 256))
	}
}

func (s *Server) writeHubError(connection net.Conn, err *hub.Error) {
	response, encodeErr := EncodeHubError(err)
	if encodeErr != nil {
		s.logger.Error("control_error_response_failed", "reason", safeText(encodeErr.Error(), 256))
		return
	}
	if writeErr := writeAll(connection, response); writeErr != nil {
		s.logger.Warn("control_error_response_lost", "code", err.Code, "reason", safeText(writeErr.Error(), 256))
	}
}

func (s *Server) writeError(connection net.Conn, code, message string) {
	response, err := EncodeError(code, message)
	if err != nil {
		return
	}
	_ = writeAll(connection, response)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func safeText(value string, max int) string {
	var output strings.Builder
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			character = ' '
		}
		if output.Len()+len(string(character)) > max {
			break
		}
		output.WriteRune(character)
	}
	return output.String()
}
