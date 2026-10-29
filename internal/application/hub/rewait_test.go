package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

type rewaitTestPlugin struct {
	*fakePlugin
	resume func(context.Context, ResumeWatchRequest) error
	grant  func(context.Context, domain.SubscriptionID, string) error
}

func (p *rewaitTestPlugin) ResumeWatch(ctx context.Context, request ResumeWatchRequest) error {
	if p.resume != nil {
		return p.resume(ctx, request)
	}
	return nil
}

func (p *rewaitTestPlugin) ActivateGeneration(ctx context.Context, id domain.SubscriptionID, generation string) error {
	if p.grant != nil {
		return p.grant(ctx, id, generation)
	}
	return nil
}

func newRewaitTestHub(t *testing.T) (*Hub, *rewaitTestPlugin, *fakePlugin) {
	t.Helper()
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	input := &rewaitTestPlugin{fakePlugin: newFakePlugin("source", domain.InputRole)}
	input.capabilities = []string{"runtime_state_v2", "rewait_v1"}
	output := newFakePlugin("sink", domain.OutputRole)
	if err := h.AddPlugin("source", domain.InputRole, input); err != nil {
		t.Fatal(err)
	}
	if err := h.AddPlugin("sink", domain.OutputRole, output); err != nil {
		t.Fatal(err)
	}
	h.StartPlugins(context.Background())
	registration := domain.Subscription{ID: "s", InputPlugin: "source", OutputPlugin: "sink", WatchArgs: testArgs(t, `{}`), DestinationArgs: testArgs(t, `{"recipient":"A"}`), Active: true}
	h.registrations["s"] = registration
	h.runtimeWatches["s"] = &runtimeWatch{inputPort: input, inputName: "source", registration: registration}
	h.notificationWaits["s"] = &notificationWait{generation: "old"}
	h.sessionStartMillis = 1000
	h.rewaitNow = func() time.Time { return time.UnixMilli(2000) }
	t.Cleanup(h.Shutdown)
	return h, input, output
}
func rewaitTestID(n int) string { return fmt.Sprintf("1500:%032x", n) }
func expectRewaitCode(t *testing.T, err *Error, code string) {
	t.Helper()
	if err == nil || err.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestRewaitOperationSyntaxAndTimeBounds(t *testing.T) {
	for _, id := range []string{"0:" + strings.Repeat("a", 32), rewaitTestID(1)} {
		if !ValidateRewaitOperationID(id) {
			t.Fatalf("invalid valid ID %q", id)
		}
	}
	for _, id := range []string{"01:" + strings.Repeat("a", 32), "-1:" + strings.Repeat("a", 32), "+1:" + strings.Repeat("a", 32), "1:" + strings.Repeat("A", 32), "1:" + strings.Repeat("a", 31), "9223372036854775808:" + strings.Repeat("a", 32)} {
		if ValidateRewaitOperationID(id) {
			t.Fatalf("accepted %q", id)
		}
	}
	h, _, _ := newRewaitTestHub(t)
	for _, stamp := range []int64{1000, 2001} {
		expectRewaitCode(t, h.Rewait("s", fmt.Sprintf("%d:%032x", stamp, 1), "p"), "operation_expired")
	}
	h.rewaitNow = func() time.Time { return time.UnixMilli(301501) }
	expectRewaitCode(t, h.Rewait("s", rewaitTestID(1), "p"), "operation_expired")
}

func TestRewaitReceiptsCommitAndGenerationAdmission(t *testing.T) {
	h, input, _ := newRewaitTestHub(t)
	var preparations, grants atomic.Int32
	input.resume = func(_ context.Context, r ResumeWatchRequest) error {
		preparations.Add(1)
		if r.SubscriptionID != "s" || r.Generation == "old" || r.ConfirmationPosition != "p1" {
			t.Errorf("prepare %+v", r)
		}
		return nil
	}
	var granted string
	input.grant = func(_ context.Context, _ domain.SubscriptionID, g string) error {
		grants.Add(1)
		granted = g
		return nil
	}
	if err := h.Rewait("s", rewaitTestID(1), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := h.Rewait("s", rewaitTestID(1), "p1"); err != nil {
		t.Fatal(err)
	}
	if preparations.Load() != 1 || grants.Load() != 1 {
		t.Fatal("retry repeated control exchange")
	}
	expectRewaitCode(t, h.Rewait("s", rewaitTestID(1), "different"), "operation_conflict")
	expectRewaitCode(t, h.Rewait("s", rewaitTestID(2), "p1"), "wait_active")
	if status := h.acceptGenerationEvent("source", input, "s", "1", "old", "stale"); status != "discarded" {
		t.Fatalf("stale status=%q", status)
	}
	if status := h.acceptGenerationEvent("source", input, "s", "2", granted, "external"); status != "" {
		t.Fatalf("fresh status=%q", status)
	}
	if status := h.acceptGenerationEvent("source", input, "s", "3", granted, "duplicate"); status != "discarded" {
		t.Fatalf("consumed status=%q", status)
	}
}

func TestRewaitPendingDuplicatesShareReceiptAndOtherRequestsStayBusy(t *testing.T) {
	h, input, _ := newRewaitTestHub(t)
	entered, release := make(chan struct{}), make(chan struct{})
	input.resume = func(context.Context, ResumeWatchRequest) error { close(entered); <-release; return nil }
	first, duplicate := make(chan *Error, 1), make(chan *Error, 1)
	go func() { first <- h.Rewait("s", rewaitTestID(1), "p") }()
	awaitActivation(t, entered)
	go func() { duplicate <- h.Rewait("s", rewaitTestID(1), "p") }()
	expectRewaitCode(t, h.Rewait("s", rewaitTestID(2), "p"), "rewait_busy")
	expectRewaitCode(t, h.Rewait("s", rewaitTestID(1), "other"), "operation_conflict")
	close(release)
	if err := awaitActivation(t, first); err != nil {
		t.Fatal(err)
	}
	if err := awaitActivation(t, duplicate); err != nil {
		t.Fatal(err)
	}
}

func TestRewaitRejectionAndUncertaintyDoNotGrant(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			h, input, _ := newRewaitTestHub(t)
			var grants atomic.Int32
			input.resume = func(context.Context, ResumeWatchRequest) error {
				if rejected {
					return &PositionRejectedError{}
				}
				return errors.New("private position must not leak")
			}
			input.grant = func(context.Context, domain.SubscriptionID, string) error { grants.Add(1); return nil }
			code := "plugin_unavailable"
			if rejected {
				code = "position_rejected"
			}
			err := h.Rewait("s", rewaitTestID(1), "p")
			expectRewaitCode(t, err, code)
			if strings.Contains(err.Message, "private") || grants.Load() != 0 {
				t.Fatal("failed preparation granted or exposed input error")
			}
			if next := h.Rewait("s", rewaitTestID(1), "p"); next != err {
				t.Fatal("failed receipt was not retained")
			}
		})
	}
}

func TestRewaitBusyValidationAndReceiptCapacity(t *testing.T) {
	h, _, _ := newRewaitTestHub(t)
	expectRewaitCode(t, h.Rewait("unknown", rewaitTestID(1), "p"), "unknown_subscription")
	for _, field := range []string{"outstanding", "checkpoint", "preparing"} {
		wait := h.notificationWaits["s"]
		wait.outstanding = field == "outstanding"
		wait.checkpointBusy = field == "checkpoint"
		wait.preparing = field == "preparing"
		expectRewaitCode(t, h.Rewait("s", rewaitTestID(1), "p"), "rewait_busy")
		if len(h.rewaitReceipts) != 0 {
			t.Fatal("busy request reserved a receipt")
		}
	}
	h.notificationWaits["s"].preparing = false
	for n := 0; n < rewaitReceiptLimit; n++ {
		h.rewaitReceipts[rewaitTestID(n)] = &rewaitReceipt{timestamp: 1500}
	}
	expectRewaitCode(t, h.Rewait("s", rewaitTestID(2000), "p"), "resource_exhausted")
	if h.notificationWaits["s"].generation != "old" {
		t.Fatal("capacity failure changed generation")
	}
}

func TestRewaitStopCancelsPreparationBeforeFinalCheckpoint(t *testing.T) {
	h, input, _ := newRewaitTestHub(t)
	entered := make(chan struct{})
	input.resume = func(ctx context.Context, _ ResumeWatchRequest) error { close(entered); <-ctx.Done(); return ctx.Err() }
	var grants atomic.Int32
	input.grant = func(context.Context, domain.SubscriptionID, string) error { grants.Add(1); return nil }
	result := make(chan *Error, 1)
	go func() { result <- h.Rewait("s", rewaitTestID(1), "p") }()
	awaitActivation(t, entered)
	h.Shutdown()
	expectRewaitCode(t, awaitActivation(t, result), "plugin_unavailable")
	if grants.Load() != 0 {
		t.Fatal("stop before commit allowed a grant")
	}
}

func TestCommittedRewaitRetainsSuccessWhenGrantSendFails(t *testing.T) {
	h, input, _ := newRewaitTestHub(t)
	var grants atomic.Int32
	input.grant = func(context.Context, domain.SubscriptionID, string) error {
		grants.Add(1)
		return errors.New("send failed")
	}
	if err := h.Rewait("s", rewaitTestID(1), "p"); err != nil {
		t.Fatalf("committed receipt changed to failure: %v", err)
	}
	if err := h.Rewait("s", rewaitTestID(1), "p"); err != nil {
		t.Fatalf("committed receipt lost: %v", err)
	}
	if grants.Load() != 1 {
		t.Fatal("retry reissued committed activation")
	}
	h.mu.Lock()
	available := h.plugins["source"].available
	h.mu.Unlock()
	if available {
		t.Fatal("failed committed grant did not invalidate input")
	}
}
