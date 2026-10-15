package codexqueue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Plugin struct {
	executable string
	service    *application.DeliveryService
	logger     *slog.Logger

	mu     sync.Mutex
	active bool
}

var _ pluginprotocol.EventPluginServer = (*Plugin)(nil)

func New(executable string, service *application.DeliveryService, logger *slog.Logger) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	return &Plugin{executable: executable, service: service, logger: logger}
}

func (p *Plugin) Session(stream pluginprotocol.SessionServer) error {
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return status.Error(codes.AlreadyExists, "an output Session is already active")
	}
	p.active = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
	}()

	if err := p.send(stream, pluginprotocol.Frame{Type: "ready", Role: "output"}); err != nil {
		return err
	}

	sessionContext, cancelSession := context.WithCancel(stream.Context())
	var inFlight sync.WaitGroup
	defer func() {
		cancelSession()
		inFlight.Wait()
	}()
	var sendMu sync.Mutex

	for {
		payload, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || stream.Context().Err() != nil {
				return nil
			}
			return err
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.DaemonToPlugin)
		if err != nil {
			p.logger.Warn("codex_queue_request_rejected", "reason", safeText(err.Error(), 256))
			return err
		}
		switch frame.Type {
		case "validate_destination":
			accepted := p.service.ValidateDestination(frame.DestinationArgs) == nil
			result := pluginprotocol.Frame{Type: "destination_result", RequestID: frame.RequestID, Accepted: &accepted}
			if !accepted {
				result.Error = "thread_id destination is invalid"
			}
			sendMu.Lock()
			err := p.send(stream, result)
			sendMu.Unlock()
			if err != nil {
				return err
			}
		case "deliver":
			requestContext, cancelRequest := context.WithTimeout(sessionContext, application.DeliveryTimeout)
			inFlight.Add(1)
			go func(request pluginprotocol.Frame) {
				defer inFlight.Done()
				defer cancelRequest()
				outcome := p.service.Deliver(requestContext, p.executable, domain.Event{
					InputPlugin:    request.InputPlugin,
					SubscriptionID: request.SubscriptionID,
					DeliveryID:     request.DeliveryID,
					Context:        request.Context,
				}, request.DestinationArgs)
				response := pluginprotocol.Frame{
					Type:      "delivery_result",
					RequestID: request.RequestID,
					Status:    outcome.Status,
				}
				if outcome.Status != "accepted" {
					response.Error = outcome.Reason
				}
				sendMu.Lock()
				sendErr := p.send(stream, response)
				sendMu.Unlock()
				p.logger.Info("codex_queue_delivery", "request_id", safeText(request.RequestID, 256),
					"delivery_id", safeText(request.DeliveryID, 256), "subscription_id", safeText(request.SubscriptionID, 256),
					"outcome", outcome.Status, "reason", outcome.Reason)
				if sendErr != nil {
					p.logger.Warn("codex_queue_result_send_failed", "request_id", safeText(request.RequestID, 256))
				}
			}(frame)
		default:
			return errors.New("unexpected daemon message")
		}
	}
}

func (p *Plugin) send(stream pluginprotocol.SessionServer, frame pluginprotocol.Frame) error {
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return err
	}
	return stream.Send(payload)
}

func safeText(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, value)
}
