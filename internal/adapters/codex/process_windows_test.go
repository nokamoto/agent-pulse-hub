//go:build windows

package codex

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"go.uber.org/mock/gomock"
)

const queueAck = "Queued message 22222222-2222-4222-8222-222222222222 for thread 11111111-1111-4111-8111-111111111111.\n"

func TestDeliveryProcessDecisionsAndFixedDeadline(t *testing.T) {
	for _, test := range []struct {
		name, stdout, stderr string
		startErr, waitErr    error
		cancelErr            error
		want                 domain.DeliveryOutcome
	}{
		{name: "accepted", stdout: queueAck, want: domain.DeliveryAccepted},
		{name: "verbose stderr", stdout: queueAck, stderr: strings.Repeat("x", MaxCapturedOutputBytes+1), want: domain.DeliveryAccepted},
		{name: "launch failure", startErr: errors.New("start"), want: domain.DeliveryFailed},
		{name: "nonzero", stdout: queueAck, waitErr: errors.New("exit 7"), want: domain.DeliveryUnknown},
		{name: "ambiguous", stdout: "queued", want: domain.DeliveryUnknown},
		{name: "lost", want: domain.DeliveryUnknown},
		{name: "wrong target", stdout: strings.ReplaceAll(queueAck, "11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"), want: domain.DeliveryUnknown},
		{name: "overflow", stdout: queueAck + strings.Repeat("x", MaxCapturedOutputBytes), want: domain.DeliveryUnknown},
		{name: "timeout", cancelErr: context.DeadlineExceeded, waitErr: errors.New("killed"), want: domain.DeliveryUnknown},
		{name: "cancelled", cancelErr: context.Canceled, waitErr: errors.New("killed"), want: domain.DeliveryUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			launcher, process, timeouts := NewMockcommandLauncher(ctrl), NewMockcommandProcess(ctrl), NewMocktimeoutSource(ctrl)
			ctx := context.Background()
			if test.cancelErr != nil {
				ctx = errorContext{Context: ctx, err: test.cancelErr}
			}
			timeouts.EXPECT().WithTimeout(gomock.Any(), 30*time.Second).Return(ctx, context.CancelFunc(func() {}))
			launcher.EXPECT().Start(ctx, `C:\codex.exe`, []string{"queue", "--thread", testEvent().SessionID, "--message", BuildEnvelope("manual", "subscription-test", "PULSE-TEST")}, gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ []string, stdout, stderr io.Writer) (commandProcess, error) {
				if test.startErr != nil {
					return nil, test.startErr
				}
				_, _ = io.WriteString(stdout, test.stdout)
				_, _ = io.WriteString(stderr, test.stderr)
				return process, nil
			})
			if test.startErr == nil {
				process.EXPECT().Wait().Return(test.waitErr)
			}
			adapter := &Adapter{executable: `C:\codex.exe`, launcher: launcher, timeouts: timeouts}
			if got := adapter.Deliver(context.Background(), testEvent()); got.Outcome != test.want {
				t.Fatalf("result = %#v, want %s", got, test.want)
			}
		})
	}
}

type errorContext struct {
	context.Context
	err error
}

func (c errorContext) Err() error { return c.err }

func TestVersionProbeProcessDecisions(t *testing.T) {
	for _, test := range []struct {
		name, output      string
		startErr, waitErr error
		wantError         bool
	}{
		{name: "different version", output: "codex-cli 9.9.9\r\n"},
		{name: "missing", startErr: errors.New("missing"), wantError: true},
		{name: "nonzero", waitErr: errors.New("exit"), wantError: true},
		{name: "empty", wantError: true},
		{name: "overflow", output: strings.Repeat("x", 257), wantError: true},
		{name: "multiple lines", output: "one\ntwo\n", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			launcher, process, timeouts := NewMockcommandLauncher(ctrl), NewMockcommandProcess(ctrl), NewMocktimeoutSource(ctrl)
			ctx := context.Background()
			timeouts.EXPECT().WithTimeout(ctx, 5*time.Second).Return(ctx, context.CancelFunc(func() {}))
			launcher.EXPECT().Start(ctx, "codex", []string{"--version"}, gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ []string, stdout, stderr io.Writer) (commandProcess, error) {
				if test.startErr != nil {
					return nil, test.startErr
				}
				_, _ = io.WriteString(stdout, test.output)
				_, _ = io.WriteString(stderr, strings.Repeat("diagnostic", 1000))
				return process, nil
			})
			if test.startErr == nil {
				process.EXPECT().Wait().Return(test.waitErr)
			}
			got, err := readVersion(ctx, "codex", launcher, timeouts)
			if (err != nil) != test.wantError {
				t.Fatalf("version = %q, error = %v", got, err)
			}
		})
	}
}

func TestDeliveryCommandLineExactBound(t *testing.T) {
	for _, size := range []int{MaxCommandLineUTF16, MaxCommandLineUTF16 + 1} {
		t.Run(string(rune(size)), func(t *testing.T) {
			ctrl := gomock.NewController(t)
			launcher, process, timeouts := NewMockcommandLauncher(ctrl), NewMockcommandProcess(ctrl), NewMocktimeoutSource(ctrl)
			event := testEvent()
			exe := `C:\codex.exe`
			base, _ := RenderCommandLine(exe, []string{"queue", "--thread", event.SessionID, "--message", BuildEnvelope(event.Plugin, string(event.SubscriptionID), "")})
			event.Context = strings.Repeat("x", size-utf16CodeUnits(base))
			if size == MaxCommandLineUTF16 {
				timeouts.EXPECT().WithTimeout(gomock.Any(), 30*time.Second).Return(context.Background(), context.CancelFunc(func() {}))
				launcher.EXPECT().Start(gomock.Any(), exe, gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, _ []string, stdout, _ io.Writer) (commandProcess, error) {
					_, _ = io.WriteString(stdout, queueAck)
					return process, nil
				})
				process.EXPECT().Wait().Return(nil)
			}
			got := (&Adapter{executable: exe, launcher: launcher, timeouts: timeouts}).Deliver(context.Background(), event)
			if (got.Outcome == domain.DeliveryAccepted) != (size == MaxCommandLineUTF16) {
				t.Fatalf("size %d result %#v", size, got)
			}
		})
	}
}
