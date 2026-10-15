package application

import (
	"context"
	"errors"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/domain"
)

const RegistrationTimeout = 25 * time.Second

type RegisterConfig struct {
	ClientExecutable string
	Address          string
	InputPlugin      string
	WatchArgsFile    string
	OutputPlugin     string
}

type Environment interface {
	Lookup(string) (string, bool)
}

type DestinationFileFactory interface {
	Create(string) (string, func() error, error)
}

//go:generate go run go.uber.org/mock/mockgen -source=registration.go -destination=registration_mock_test.go -package=application

type RegistrationResult struct {
	Status         string
	Reason         string
	Response       []byte
	SubscriptionID string
	Code           string
	Message        string
	CleanupError   error
}

type RegistrationService struct {
	runner ProcessRunner
	files  DestinationFileFactory
}

func NewRegistrationService(runner ProcessRunner, files DestinationFileFactory) *RegistrationService {
	return &RegistrationService{runner: runner, files: files}
}

func (s *RegistrationService) RegisterCurrent(parent context.Context, config RegisterConfig, environment Environment) (result RegistrationResult) {
	threadID, threadOK := environment.Lookup("CODEX_THREAD_ID")
	sessionID, sessionOK := environment.Lookup("CODEX_SESSION_ID")
	if !threadOK || !sessionOK || !domain.IsCanonicalUUID(threadID) || !domain.IsCanonicalUUID(sessionID) || threadID != sessionID {
		return RegistrationResult{Status: "failed", Reason: "codex_identity_invalid"}
	}

	destinationPath, cleanup, err := s.files.Create(threadID)
	if err != nil {
		return RegistrationResult{Status: "failed", Reason: "registration_destination_unavailable"}
	}
	defer func() {
		if cleanup != nil {
			result.CleanupError = cleanup()
		}
	}()

	ctx, cancel := context.WithTimeout(parent, RegistrationTimeout)
	defer cancel()
	args := []string{
		"register",
		"--input-plugin", config.InputPlugin,
		"--watch-args-file", config.WatchArgsFile,
		"--output-plugin", config.OutputPlugin,
		"--destination-args-file", destinationPath,
		"--address", config.Address,
	}
	process := s.runner.Run(ctx, config.ClientExecutable, args)
	if !process.Started {
		return RegistrationResult{Status: "failed", Reason: "registration_client_start_failed"}
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return RegistrationResult{Status: "unknown", Reason: "registration_client_timeout"}
		}
		return RegistrationResult{Status: "unknown", Reason: "registration_client_interrupted"}
	}
	if process.StdoutOverflow || process.StderrOverflow {
		return RegistrationResult{Status: "unknown", Reason: "registration_client_output_overflow"}
	}
	reply, err := domain.ParseClientReply(process.Stdout)
	if err != nil {
		return RegistrationResult{Status: "unknown", Reason: "registration_client_result_unconfirmed"}
	}
	if reply.OK && process.Exited && process.ExitCode == 0 && process.Err == nil {
		return RegistrationResult{Status: "accepted", Response: append([]byte(nil), process.Stdout...), SubscriptionID: reply.SubscriptionID}
	}
	if !reply.OK && process.Exited && process.ExitCode == 1 {
		return RegistrationResult{Status: "failed", Code: reply.Code, Message: reply.Message}
	}
	return RegistrationResult{Status: "unknown", Reason: "registration_client_result_unconfirmed"}
}
