package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

const (
	registerThread       = "11111111-1111-4111-8111-111111111111"
	temporaryDestination = "temporary-destination.json"
)

func mockEnvironment(controller *gomock.Controller, values map[string]string) *MockEnvironment {
	environment := NewMockEnvironment(controller)
	for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID"} {
		value, ok := values[key]
		environment.EXPECT().Lookup(key).Return(value, ok)
	}
	return environment
}

func TestRegisterCurrentRequiresEqualCanonicalInvokingIdentityBeforeClient(t *testing.T) {
	for index, identity := range []map[string]string{
		{"CODEX_SESSION_ID": registerThread},
		{"CODEX_THREAD_ID": registerThread},
		{"CODEX_THREAD_ID": "not-a-uuid", "CODEX_SESSION_ID": registerThread},
		{"CODEX_THREAD_ID": "00000000-0000-0000-0000-000000000000", "CODEX_SESSION_ID": "00000000-0000-0000-0000-000000000000"},
		{"CODEX_THREAD_ID": registerThread, "CODEX_SESSION_ID": "22222222-2222-4222-8222-222222222222"},
	} {
		t.Run(fmt.Sprintf("invalid identity %d", index+1), func(t *testing.T) {
			controller := gomock.NewController(t)
			runner := NewMockProcessRunner(controller)
			files := NewMockDestinationFileFactory(controller)
			result := NewRegistrationService(runner, files).RegisterCurrent(context.Background(), RegisterConfig{}, mockEnvironment(controller, identity))
			if result.Status != "failed" || result.Reason != "codex_identity_invalid" {
				t.Fatalf("result=%+v for %v", result, identity)
			}
		})
	}
}

func TestRegisterCurrentUsesOneClientAndPreservesOnlyConfirmedSuccess(t *testing.T) {
	config := RegisterConfig{
		ClientExecutable: "hub", Address: "127.0.0.1:9000", InputPlugin: "manual",
		WatchArgsFile: "watch.json", OutputPlugin: "codex",
	}
	for _, test := range []struct {
		name     string
		process  ProcessResult
		status   string
		reason   string
		code     string
		message  string
		subID    string
		response []byte
	}{
		{name: "success", process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte("{\"ok\":true,\"subscription_id\":\"sub-α\"}\n")}, status: "accepted", subID: "sub-α", response: []byte("{\"ok\":true,\"subscription_id\":\"sub-α\"}\n")},
		{name: "explicit rejection", process: ProcessResult{Started: true, Exited: true, ExitCode: 1, Stdout: []byte(`{"ok":false,"code":"invalid","message":"rejected"}`), Err: errors.New("exit status 1")}, status: "failed", code: "invalid", message: "rejected"},
		{name: "truncated output is unknown", process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte(`{"ok":true,"subscription_id":`)}, status: "unknown", reason: "registration_client_result_unconfirmed"},
		{name: "overflow is unknown", process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte(`{"ok":true,"subscription_id":"sub"}`), StderrOverflow: true}, status: "unknown", reason: "registration_client_output_overflow"},
		{name: "contradictory success exit is unknown", process: ProcessResult{Started: true, Exited: true, ExitCode: 2, Stdout: []byte(`{"ok":true,"subscription_id":"sub"}`), Err: errors.New("exit status 2")}, status: "unknown", reason: "registration_client_result_unconfirmed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			runner := NewMockProcessRunner(controller)
			files := NewMockDestinationFileFactory(controller)
			cleanupCalls := 0
			files.EXPECT().Create(registerThread).Return(temporaryDestination, func() error {
				cleanupCalls++
				return nil
			}, nil)
			wantArgs := []string{
				"register", "--input-plugin", "manual", "--watch-args-file", "watch.json",
				"--output-plugin", "codex", "--destination-args-file", temporaryDestination,
				"--address", "127.0.0.1:9000",
			}
			calls := 0
			runner.EXPECT().Run(gomock.Any(), "hub", wantArgs).DoAndReturn(func(ctx context.Context, _ string, _ []string) ProcessResult {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > RegistrationTimeout {
					t.Errorf("registration context deadline=%v present=%t", deadline, ok)
				}
				return test.process
			})
			service := NewRegistrationService(runner, files)
			result := service.RegisterCurrent(context.Background(), config, mockEnvironment(controller, map[string]string{
				"CODEX_THREAD_ID": registerThread, "CODEX_SESSION_ID": registerThread,
			}))
			if calls != 1 || cleanupCalls != 1 {
				t.Fatalf("client calls=%d cleanup calls=%d", calls, cleanupCalls)
			}
			if result.Status != test.status || result.Reason != test.reason || result.Code != test.code || result.Message != test.message || result.SubscriptionID != test.subID || string(result.Response) != string(test.response) {
				t.Fatalf("result=%+v want status=%s reason=%s code=%s message=%s subscription=%s response=%q", result, test.status, test.reason, test.code, test.message, test.subID, test.response)
			}
		})
	}
}

func TestRegisterCurrentCancellationAfterClientStartIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := gomock.NewController(t)
	runner := NewMockProcessRunner(controller)
	files := NewMockDestinationFileFactory(controller)
	cleanupCalls := 0
	files.EXPECT().Create(registerThread).Return(temporaryDestination, func() error {
		cleanupCalls++
		return nil
	}, nil)
	runner.EXPECT().Run(gomock.Any(), "hub", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ []string) ProcessResult {
		cancel()
		return ProcessResult{Started: true, Exited: true, ExitCode: -1}
	})
	result := NewRegistrationService(runner, files).RegisterCurrent(ctx, RegisterConfig{ClientExecutable: "hub"}, mockEnvironment(controller, map[string]string{
		"CODEX_THREAD_ID": registerThread, "CODEX_SESSION_ID": registerThread,
	}))
	if result.Status != "unknown" || result.Reason != "registration_client_interrupted" || cleanupCalls != 1 {
		t.Fatalf("result=%+v cleanup calls=%d", result, cleanupCalls)
	}
}

func TestRegisterCurrentCancellationBeforeClientLaunchIsFailedAndCleansDestination(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	controller := gomock.NewController(t)
	runner := NewMockProcessRunner(controller)
	files := NewMockDestinationFileFactory(controller)
	cleanupCalls := 0
	files.EXPECT().Create(registerThread).Return(temporaryDestination, func() error {
		cleanupCalls++
		return nil
	}, nil)
	runner.EXPECT().Run(gomock.Any(), "hub", gomock.Any()).DoAndReturn(func(processContext context.Context, _ string, _ []string) ProcessResult {
		if processContext.Err() != context.Canceled {
			t.Errorf("client context error=%v want %v", processContext.Err(), context.Canceled)
		}
		return ProcessResult{Err: context.Canceled}
	})
	result := NewRegistrationService(runner, files).RegisterCurrent(ctx, RegisterConfig{ClientExecutable: "hub"}, mockEnvironment(controller, map[string]string{
		"CODEX_THREAD_ID": registerThread, "CODEX_SESSION_ID": registerThread,
	}))
	if result.Status != "failed" || result.Reason != "registration_client_start_failed" || cleanupCalls != 1 {
		t.Fatalf("result=%+v cleanup calls=%d", result, cleanupCalls)
	}
}

func TestRegisterCurrentCleanupFailureDoesNotChangeOutcome(t *testing.T) {
	controller := gomock.NewController(t)
	runner := NewMockProcessRunner(controller)
	files := NewMockDestinationFileFactory(controller)
	files.EXPECT().Create(registerThread).Return(temporaryDestination, func() error { return errors.New("cleanup failed") }, nil)
	runner.EXPECT().Run(gomock.Any(), "hub", gomock.Any()).Return(ProcessResult{
		Started: true, Exited: true, ExitCode: 0, Stdout: []byte(`{"ok":true,"subscription_id":"sub"}`),
	})
	result := NewRegistrationService(runner, files).RegisterCurrent(context.Background(), RegisterConfig{ClientExecutable: "hub"}, mockEnvironment(controller, map[string]string{
		"CODEX_THREAD_ID": registerThread, "CODEX_SESSION_ID": registerThread,
	}))
	if result.Status != "accepted" || result.CleanupError == nil || result.SubscriptionID != "sub" {
		t.Fatalf("cleanup changed known registration result: %+v", result)
	}
}

func TestRegisterCurrentReportsTemporaryDestinationCreationFailure(t *testing.T) {
	controller := gomock.NewController(t)
	files := NewMockDestinationFileFactory(controller)
	files.EXPECT().Create(registerThread).Return("", nil, errors.New("cannot create temporary file"))
	result := NewRegistrationService(NewMockProcessRunner(controller), files).RegisterCurrent(context.Background(), RegisterConfig{}, mockEnvironment(controller, map[string]string{
		"CODEX_THREAD_ID": registerThread, "CODEX_SESSION_ID": registerThread,
	}))
	if result.Status != "failed" || result.Reason != "registration_destination_unavailable" {
		t.Fatalf("result=%+v", result)
	}
}
