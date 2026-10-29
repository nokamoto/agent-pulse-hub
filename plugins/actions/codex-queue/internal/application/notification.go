package application

import (
	"context"
	"errors"
	"strings"

	shared "github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/domain"
)

func (s *DeliveryService) DeliverNotification(parent context.Context, executable string, event domain.Event, args *jsonvalue.Value) shared.AttemptEvidence {
	ctx, cancel := context.WithTimeout(parent, DeliveryTimeout)
	defer cancel()
	failure := func(status, reason string, retryable bool) shared.AttemptEvidence {
		message := "Codex queue attempt did not confirm acceptance (" + reason + ")."
		e := shared.AttemptEvidence{Status: status, Reason: &reason, Message: &message}
		if status == "unsubmitted" {
			e.Retryable = &retryable
		}
		return e
	}
	destination, err := domain.ParseDestination(args)
	if err != nil {
		return failure("unsubmitted", "codex_destination_invalid", false)
	}
	message, err := domain.EncodeEnvelope(event)
	if err != nil {
		return failure("unsubmitted", "codex_envelope_invalid", false)
	}
	if ctx.Err() != nil {
		return failure("unsubmitted", "codex_cancelled", false)
	}
	process := s.runner.Run(ctx, executable, []string{"queue", "--thread", destination.ThreadID, "--message", message})
	if !process.Started {
		if process.StartRetryable {
			return failure("unsubmitted", "codex_start_resource", true)
		}
		return failure("unsubmitted", "codex_start_failed", false)
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return failure("unknown", "codex_timeout", false)
		}
		return failure("unknown", "codex_interrupted", false)
	}
	if process.StdoutOverflow || process.StderrOverflow {
		return failure("unknown", "codex_output_overflow", false)
	}
	if !process.Exited || process.Err != nil || process.ExitCode != 0 {
		return failure("unknown", "codex_exit_unconfirmed", false)
	}
	if err := domain.ParseAcknowledgement(process.Stdout, destination.ThreadID); err != nil {
		var ack *domain.AckError
		if errors.As(err, &ack) {
			return failure("unknown", ack.Reason, false)
		}
		return failure("unknown", "codex_ack_invalid", false)
	}
	line := strings.TrimPrefix(string(process.Stdout), domain.AckPrefix)
	receipt := strings.SplitN(line, domain.AckSeparator, 2)[0]
	return shared.AttemptEvidence{Status: "accepted", Receipt: &receipt}
}
