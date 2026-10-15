package application

import (
	"context"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/domain"
	"go.uber.org/mock/gomock"
)

const (
	testThreadA = "11111111-1111-4111-8111-111111111111"
	testThreadB = "22222222-2222-4222-8222-222222222222"
)

func destinationValue(t *testing.T, text string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(text), 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDeliveryUsesOneExactQueueInvocationAndClassifiesOutcomes(t *testing.T) {
	ack := []byte("Queued message queue-1 for thread " + testThreadA + ".\n")
	event := domain.Event{InputPlugin: "manual", SubscriptionID: "sub-a", DeliveryID: "delivery-a", Context: "hello"}
	for _, test := range []struct {
		name       string
		args       *jsonvalue.Value
		process    ProcessResult
		wantStatus string
		wantReason string
		wantCalls  int
	}{
		{name: "invalid destination never launches", args: destinationValue(t, `{}`), wantStatus: "failed", wantReason: "codex_destination_invalid"},
		{name: "start failure is failed", args: destinationValue(t, `{"thread_id":"`+testThreadA+`"}`), process: ProcessResult{ExitCode: -1}, wantStatus: "failed", wantReason: "codex_start_failed", wantCalls: 1},
		{name: "matching ACK is accepted", args: destinationValue(t, `{"thread_id":"`+testThreadA+`"}`), process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: ack}, wantStatus: "accepted", wantCalls: 1},
		{name: "nonzero exit stays unknown despite plausible ACK", args: destinationValue(t, `{"thread_id":"`+testThreadA+`"}`), process: ProcessResult{Started: true, Exited: true, ExitCode: 1, Stdout: ack}, wantStatus: "unknown", wantReason: "codex_exit_unconfirmed", wantCalls: 1},
		{name: "wrong target stays unknown", args: destinationValue(t, `{"thread_id":"`+testThreadA+`"}`), process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte("Queued message queue-1 for thread " + testThreadB + ".")}, wantStatus: "unknown", wantReason: "codex_ack_target_mismatch", wantCalls: 1},
		{name: "capture overflow stays unknown", args: destinationValue(t, `{"thread_id":"`+testThreadA+`"}`), process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: ack, StderrOverflow: true}, wantStatus: "unknown", wantReason: "codex_output_overflow", wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := NewMockProcessRunner(gomock.NewController(t))
			if test.wantCalls != 0 {
				runner.EXPECT().Run(gomock.Any(), "codex", gomock.Any()).DoAndReturn(func(_ context.Context, executable string, args []string) ProcessResult {
					if executable != "codex" {
						t.Errorf("executable=%q", executable)
					}
					if len(args) != 5 || args[0] != "queue" || args[1] != "--thread" || args[2] != testThreadA || args[3] != "--message" {
						t.Errorf("argv=%q", args)
					}
					if !strings.HasPrefix(args[4], domain.EnvelopeLead) {
						t.Errorf("message lacks fixed envelope prefix: %q", args[4])
					}
					return test.process
				}).Times(test.wantCalls)
			}
			result := NewDeliveryService(runner).Deliver(context.Background(), "codex", event, test.args)
			if result.Status != test.wantStatus || result.Reason != test.wantReason {
				t.Fatalf("result=%+v want status=%s reason=%s calls=%d", result, test.wantStatus, test.wantReason, test.wantCalls)
			}
		})
	}
}

func TestDeliveryCancellationAfterStartIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := NewMockProcessRunner(gomock.NewController(t))
	runner.EXPECT().Run(gomock.Any(), "codex", gomock.Any()).DoAndReturn(func(context.Context, string, []string) ProcessResult {
		cancel()
		return ProcessResult{Started: true, Exited: true, ExitCode: -1}
	})
	result := NewDeliveryService(runner).Deliver(ctx, "codex", domain.Event{
		InputPlugin: "manual", SubscriptionID: "sub", DeliveryID: "delivery", Context: "context",
	}, destinationValue(t, `{"thread_id":"`+testThreadA+`"}`))
	if result.Status != "unknown" || result.Reason != "codex_interrupted" {
		t.Fatalf("result=%+v want interrupted unknown", result)
	}
}

func TestDeliveryCancellationBeforeLaunchIsFailed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := NewMockProcessRunner(gomock.NewController(t))
	runner.EXPECT().Run(gomock.Any(), "codex", gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ []string) ProcessResult {
		if ctx.Err() != context.Canceled {
			t.Errorf("process context error=%v want %v", ctx.Err(), context.Canceled)
		}
		return ProcessResult{Err: context.Canceled}
	})
	result := NewDeliveryService(runner).Deliver(ctx, "codex", domain.Event{
		InputPlugin: "manual", SubscriptionID: "sub", DeliveryID: "delivery", Context: "context",
	}, destinationValue(t, `{"thread_id":"`+testThreadA+`"}`))
	if result.Status != "failed" || result.Reason != "codex_start_failed" {
		t.Fatalf("result=%+v want failed before process start", result)
	}
}

func TestVerifyCodexCompatibilityRequiresBoundedSuccessfulHelp(t *testing.T) {
	for _, test := range []struct {
		name    string
		process ProcessResult
		ok      bool
	}{
		{name: "required options", process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte("queue --thread X --message Y")}, ok: true},
		{name: "missing option", process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte("queue --thread X")}},
		{name: "nonzero exit", process: ProcessResult{Started: true, Exited: true, ExitCode: 1, Stdout: []byte("--thread --message")}},
		{name: "overflow", process: ProcessResult{Started: true, Exited: true, ExitCode: 0, Stdout: []byte("--thread --message"), StdoutOverflow: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := NewMockProcessRunner(gomock.NewController(t))
			runner.EXPECT().Run(gomock.Any(), "codex", []string{"queue", "--help"}).Return(test.process)
			err := VerifyCodexCompatibility(context.Background(), "codex", runner)
			if (err == nil) != test.ok {
				t.Fatalf("VerifyCodexCompatibility() error=%v want success=%t", err, test.ok)
			}
		})
	}
}
