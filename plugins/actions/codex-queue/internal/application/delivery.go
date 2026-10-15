package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/domain"
)

const DeliveryTimeout = 20 * time.Second

type DeliveryResult struct {
	Status string
	Reason string
}

type DeliveryService struct {
	runner ProcessRunner
}

func NewDeliveryService(runner ProcessRunner) *DeliveryService {
	return &DeliveryService{runner: runner}
}

func (s *DeliveryService) ValidateDestination(arguments *jsonvalue.Value) error {
	_, err := domain.ParseDestination(arguments)
	return err
}

func (s *DeliveryService) Deliver(parent context.Context, executable string, event domain.Event, arguments *jsonvalue.Value) DeliveryResult {
	ctx, cancel := context.WithTimeout(parent, DeliveryTimeout)
	defer cancel()

	destination, err := domain.ParseDestination(arguments)
	if err != nil {
		return DeliveryResult{Status: "failed", Reason: "codex_destination_invalid"}
	}
	message, err := domain.EncodeEnvelope(event)
	if err != nil {
		return DeliveryResult{Status: "failed", Reason: "codex_envelope_invalid"}
	}

	process := s.runner.Run(ctx, executable, []string{"queue", "--thread", destination.ThreadID, "--message", message})
	if !process.Started {
		return DeliveryResult{Status: "failed", Reason: "codex_start_failed"}
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return DeliveryResult{Status: "unknown", Reason: "codex_timeout"}
		}
		return DeliveryResult{Status: "unknown", Reason: "codex_interrupted"}
	}
	if process.StdoutOverflow || process.StderrOverflow {
		return DeliveryResult{Status: "unknown", Reason: "codex_output_overflow"}
	}
	if !process.Exited || process.Err != nil || process.ExitCode != 0 {
		return DeliveryResult{Status: "unknown", Reason: "codex_exit_unconfirmed"}
	}
	if err := domain.ParseAcknowledgement(process.Stdout, destination.ThreadID); err != nil {
		var ackErr *domain.AckError
		if errors.As(err, &ackErr) {
			return DeliveryResult{Status: "unknown", Reason: ackErr.Reason}
		}
		return DeliveryResult{Status: "unknown", Reason: "codex_ack_invalid"}
	}
	return DeliveryResult{Status: "accepted"}
}

func VerifyCodexCompatibility(ctx context.Context, executable string, runner ProcessRunner) error {
	process := runner.Run(ctx, executable, []string{"queue", "--help"})
	if !process.Started {
		return errors.New("codex executable could not start queue --help")
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("codex queue --help exceeded the five-second readiness bound")
		}
		return errors.New("codex queue --help was interrupted")
	}
	if process.StdoutOverflow || process.StderrOverflow {
		return errors.New("codex queue --help output exceeded 64 KiB")
	}
	if !process.Exited || process.Err != nil || process.ExitCode != 0 {
		return errors.New("codex queue --help did not exit successfully")
	}
	if !containsOption(process.Stdout, "--thread") || !containsOption(process.Stdout, "--message") {
		return errors.New("codex queue --help must expose --thread and --message")
	}
	return nil
}

func containsOption(output []byte, option string) bool {
	return stringContains(string(output), option)
}

func stringContains(value, part string) bool {
	if len(part) == 0 {
		return true
	}
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}

func (r DeliveryResult) Error() error {
	if r.Status == "accepted" {
		return nil
	}
	if r.Reason == "" {
		return fmt.Errorf("codex delivery outcome is %s", r.Status)
	}
	return fmt.Errorf("%s", r.Reason)
}
