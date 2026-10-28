package application

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/domain"
	"go.uber.org/mock/gomock"
)

func TestNotificationQueueStartBoundaryAndTargetMapping(t *testing.T) {
	for _, test := range []struct {
		name    string
		process ProcessResult
		status  string
		retry   bool
	}{
		{"missing", ProcessResult{}, "unsubmitted", false},
		{"temporary_start", ProcessResult{StartRetryable: true}, "unsubmitted", true},
		{"accepted", ProcessResult{Started: true, Exited: true, Stdout: []byte("Queued message queue-1 for thread " + testThreadA + ".\n")}, "accepted", false},
		{"nonzero_with_ack", ProcessResult{Started: true, Exited: true, ExitCode: 1, Stdout: []byte("Queued message queue-1 for thread " + testThreadA + ".\n")}, "unknown", false},
		{"wrong_target", ProcessResult{Started: true, Exited: true, Stdout: []byte("Queued message queue-1 for thread " + testThreadB + ".\n")}, "unknown", false},
		{"ack_lost", ProcessResult{Started: true, Exited: true}, "unknown", false},
		{"capture_failed", ProcessResult{Started: true, Exited: true, StdoutOverflow: true}, "unknown", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := NewMockProcessRunner(gomock.NewController(t))
			runner.EXPECT().Run(gomock.Any(), "codex", gomock.Any()).DoAndReturn(func(_ context.Context, _ string, argv []string) ProcessResult {
				if len(argv) != 5 || argv[2] != testThreadA || !strings.Contains(argv[4], "retarget B") {
					t.Fatal("original target or context changed")
				}
				return test.process
			}).Times(1)
			args := destinationValue(t, `{"thread_id":"`+testThreadA+`"}`)
			result := NewDeliveryService(runner).DeliverNotification(context.Background(), "codex", domain.Event{InputPlugin: "manual", SubscriptionID: "sub", DeliveryID: "notification", Context: "retarget B"}, args)
			if result.Status != test.status || result.Retryable != nil && *result.Retryable != test.retry {
				t.Fatalf("result=%+v", result)
			}
			if test.status == "accepted" {
				if result.Receipt == nil || *result.Receipt != "queue-1" {
					t.Fatal("missing queue receipt")
				}
			} else {
				if result.Message == nil || strings.Contains(*result.Message, "retarget B") || result.Reason == nil {
					t.Fatal("unsafe failure evidence")
				}
			}
		})
	}
}

func TestNotificationCancelledOrExpiredContextNeverLaunches(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "expired"}[deadline], func(t *testing.T) {
			runner := NewMockProcessRunner(gomock.NewController(t))
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
			}
			cancel()
			result := NewDeliveryService(runner).DeliverNotification(ctx, "codex", domain.Event{InputPlugin: "input", SubscriptionID: "sub", DeliveryID: "id", Context: "data"}, destinationValue(t, `{"thread_id":"`+testThreadA+`"}`))
			if result.Status != "unsubmitted" || result.Retryable == nil || *result.Retryable {
				t.Fatalf("cancelled prelaunch result=%+v", result)
			}
		})
	}
}
