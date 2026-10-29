package control

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

type Server struct {
	listener net.Listener
	service  *hub.Hub
	logger   *slog.Logger
	done     chan struct{}

	mu     sync.Mutex
	closed bool
	active map[net.Conn]struct{}
}

func NewServer(address string, service *hub.Hub, logger *slog.Logger) (*Server, error) {
	if err := validateAddress(address); err != nil {
		return nil, err
	}
	if service == nil {
		return nil, errors.New("control service is required")
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, fmt.Errorf("reserve control address: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{listener: listener, service: service, logger: logger, done: make(chan struct{}), active: make(map[net.Conn]struct{})}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	stopCancellation := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stopCancellation()
	if ctx.Err() != nil {
		_ = s.Close()
		return nil
	}
	var handlers sync.WaitGroup
	var serveErr error
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				serveErr = fmt.Errorf("accept control connection: %w", err)
			}
			break
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = connection.Close()
			continue
		}
		s.active[connection] = struct{}{}
		s.mu.Unlock()
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer s.removeConnection(connection)
			s.handle(connection)
		}()
	}
	_ = s.Close()
	handlers.Wait()
	return serveErr
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	connections := make([]net.Conn, 0, len(s.active))
	for connection := range s.active {
		connections = append(connections, connection)
	}
	s.mu.Unlock()
	err := s.listener.Close()
	for _, connection := range connections {
		_ = connection.Close()
	}
	return err
}

func (s *Server) removeConnection(connection net.Conn) {
	s.mu.Lock()
	delete(s.active, connection)
	s.mu.Unlock()
	_ = connection.Close()
}

func (s *Server) handle(connection net.Conn) {
	deadline := time.Now().Add(operationTimeout)
	if err := connection.SetDeadline(deadline); err != nil {
		return
	}
	frame, err := protocol.ReadFrame(bufio.NewReader(connection), protocol.MaxFrameBytes)
	if err != nil {
		if errors.Is(err, protocol.ErrFrameTooLarge) {
			s.writeError(connection, &hub.Error{Code: "resource_exhausted", Message: "Control request exceeds the frame limit."})
		} else if !errors.Is(err, io.EOF) {
			s.writeError(connection, &hub.Error{Code: "invalid_json", Message: "Control request frame is incomplete."})
		}
		return
	}
	request, requestErr := parseRequest(frame)
	if requestErr != nil {
		s.writeError(connection, requestErr)
		return
	}
	if request.operation == "notification" || request.operation == "notifications" {
		wire, inspectionErr := notificationResponse(s.service, request)
		if inspectionErr != nil {
			s.writeError(connection, inspectionErr)
			return
		}
		_, _ = writeRequest(connection, wire)
		return
	}
	if request.operation == "unsubscribe" {
		completed := make(chan *hub.Error, 1)
		go func() { completed <- s.service.Unsubscribe(request.subscriptionID) }()
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
		case operationErr := <-completed:
			if operationErr != nil {
				s.writeError(connection, operationErr)
				return
			}
			response, err := EncodeSuccess(request.subscriptionID)
			if err == nil {
				_, _ = writeRequest(connection, response)
			}
		}
		return
	}
	if request.operation == "stop" {
		// Hub shutdown closes registration and event admission and resolves any
		// in-flight delivery before the public stop acknowledgement is written.
		s.service.Shutdown()
		if _, err := writeRequest(connection, []byte("{\"ok\":true}\n")); err != nil {
			s.logger.Warn("control_stop_response_lost", "reason", safeText(err.Error(), 256))
		}
		_ = s.Close()
		return
	}
	if request.operation == "rewait" {
		completed := make(chan *hub.Error, 1)
		go func() {
			completed <- s.service.Rewait(request.subscriptionID, request.operationID, request.confirmationPosition)
		}()
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		select {
		case <-s.done:
			return
		case <-timer.C:
			return
		case operationErr := <-completed:
			if operationErr != nil {
				s.writeError(connection, operationErr)
				return
			}
			response, err := EncodeRewaitSuccess(request.subscriptionID, request.operationID)
			if err != nil {
				s.logger.Error("control_response_error", "reason", "could not encode rewait response")
				return
			}
			if _, err := writeRequest(connection, response); err != nil {
				s.logger.Warn("control_rewait_response_lost", "subscription_id", request.subscriptionID, "reason", safeText(err.Error(), 256))
			}
		}
		return
	}
	// Hub registration owns its plugin operation. Waiting on the server signal
	// lets composition shut the Hub down after Serve returns.
	type result struct {
		subscriptionID string
		err            *hub.Error
	}
	completed := make(chan result, 1)
	go func() {
		id, err := s.service.Register(
			request.registration.InputPlugin,
			request.registration.WatchArgs,
			request.registration.OutputPlugin,
			request.registration.DestinationArgs,
		)
		completed <- result{subscriptionID: string(id), err: err}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-s.done:
		return
	case <-timer.C:
		return
	case registration := <-completed:
		if registration.err != nil {
			s.writeError(connection, registration.err)
			return
		}
		response, err := EncodeSuccess(registration.subscriptionID)
		if err != nil {
			s.logger.Error("control_response_error", "reason", safeText(err.Error(), 256))
			return
		}
		if _, err := writeRequest(connection, response); err != nil {
			s.logger.Warn("control_response_lost", "input_plugin", request.registration.InputPlugin, "output_plugin", request.registration.OutputPlugin, "subscription_id", registration.subscriptionID, "reason", safeText(err.Error(), 256))
		}
	}
}

func (s *Server) writeError(connection net.Conn, responseErr *hub.Error) {
	response, err := EncodeHubError(responseErr)
	if err != nil {
		s.logger.Error("control_error_response_failed", "reason", safeText(err.Error(), 256))
		return
	}
	if _, err := writeRequest(connection, response); err != nil {
		s.logger.Warn("control_error_response_lost", "code", responseErr.Code, "reason", safeText(err.Error(), 256))
	}
}

func safeText(value string, maxBytes int) string {
	var output strings.Builder
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			character = ' '
		}
		if output.Len()+len(string(character)) > maxBytes {
			break
		}
		output.WriteRune(character)
	}
	return output.String()
}
