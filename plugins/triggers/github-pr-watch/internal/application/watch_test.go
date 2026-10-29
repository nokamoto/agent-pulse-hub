package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
	"go.uber.org/mock/gomock"
)

func args(t *testing.T) *jsonvalue.Value {
	t.Helper()
	v, err := jsonvalue.Parse([]byte(`{"host":"github.com","owner":"o","repository":"r","pull_number":1,"waiting_reason":"wait","represented_actor":"reviewer"}`), 4096)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestValidateDoesNotAccessCredentialsForInvalidArguments(t *testing.T) {
	ctrl := gomock.NewController(t)
	_, err := Validate(context.Background(), args(t), jsonvalue.NewNull(), "different.test", make([]byte, 16), NewMockCredentials(ctrl), NewMockGitHub(ctrl))
	if err == nil {
		t.Fatal("mismatched host accepted")
	}
}

func TestValidateRejectsRestoredScopeBeforeExternalCalls(t *testing.T) {
	ctrl := gomock.NewController(t)
	scope, _ := domain.ParseWatch(args(t), "github.com")
	scope.PullNumber = 2
	tracker, _ := domain.NewTracker(scope, make([]byte, 16))
	state, _ := tracker.Checkpoint()
	_, err := Validate(context.Background(), args(t), state, "github.com", make([]byte, 16), NewMockCredentials(ctrl), NewMockGitHub(ctrl))
	if err == nil {
		t.Fatal("foreign saved scope accepted")
	}
}

func TestValidateSelectedCredentialRejectionDoesNotFallBack(t *testing.T) {
	ctrl := gomock.NewController(t)
	credentials := NewMockCredentials(ctrl)
	github := NewMockGitHub(ctrl)
	ctx := context.Background()
	credentials.EXPECT().Token(ctx, "github.com").Return("selected-token", nil)
	github.EXPECT().Validate(ctx, gomock.Any(), "selected-token").Return(&Failure{Code: "authentication_rejected"})
	_, err := Validate(ctx, args(t), jsonvalue.NewNull(), "github.com", make([]byte, 16), credentials, github)
	if err == nil || err.Error() != "authentication_rejected" {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateCapabilityCheckDoesNotEstablishBaseline(t *testing.T) {
	ctrl := gomock.NewController(t)
	credentials := NewMockCredentials(ctrl)
	github := NewMockGitHub(ctrl)
	ctx := context.Background()
	credentials.EXPECT().Token(ctx, "github.com").Return("token", nil)
	github.EXPECT().Validate(ctx, gomock.Any(), "token").Return(nil)
	validated, err := Validate(ctx, args(t), jsonvalue.NewNull(), "github.com", make([]byte, 16), credentials, github)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Tracker.Saved.Baseline {
		t.Fatal("access validation established baseline")
	}
}

func TestRetryDelay(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		err    error
		level  uint
		jitter float64
		want   time.Duration
	}{{"healthy", nil, 0, 0, time.Minute}, {"permanent", &Failure{Code: "access_denied"}, 0, 0, time.Minute}, {"network", &Failure{Code: "network"}, 0, 0, 5 * time.Second}, {"timeout", &Failure{Code: "timeout"}, 1, 1, 12 * time.Second}, {"capped", &Failure{Code: "service"}, 8, 0, time.Minute}, {"rate reset", &Failure{Code: "rate_limit", RetryAt: now.Add(3 * time.Minute)}, 0, 0, 3 * time.Minute}, {"unclassified", errors.New("x"), 0, 0, time.Minute}} {
		t.Run(test.name, func(t *testing.T) {
			if got := RetryDelay(time.Minute, test.err, test.level, now, test.jitter); got != test.want {
				t.Fatalf("delay=%s want=%s", got, test.want)
			}
		})
	}
}
