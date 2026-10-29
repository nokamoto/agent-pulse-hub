package application

import (
	"context"
	"errors"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

//go:generate go run go.uber.org/mock/mockgen -source=watch.go -destination=watch_mock_test.go -package=application

type Credentials interface {
	Token(context.Context, string) (string, error)
}
type GitHub interface {
	Validate(context.Context, domain.WatchConfig, string) error
	Observe(context.Context, domain.WatchConfig, string) (domain.Observation, error)
}
type Failure struct {
	Code       string
	StatusCode int
	RetryAt    time.Time
}

func (f *Failure) Error() string { return f.Code }

type Validated struct {
	Config  domain.WatchConfig
	Token   string
	Tracker *domain.Tracker
}

func Validate(ctx context.Context, args, state *jsonvalue.Value, host string, nonce []byte, credentials Credentials, github GitHub) (Validated, error) {
	config, err := domain.ParseWatch(args, host)
	if err != nil {
		return Validated{}, err
	}
	tracker, err := domain.NewTracker(config, nonce)
	if err != nil {
		return Validated{}, err
	}
	if state != nil && state.Kind() != jsonvalue.Null {
		tracker, err = domain.Restore(config, state)
		if err != nil {
			return Validated{}, err
		}
	}
	token, err := credentials.Token(ctx, host)
	if err != nil {
		return Validated{}, errors.New("credential_unavailable")
	}
	if err := github.Validate(ctx, config, token); err != nil {
		return Validated{}, err
	}
	return Validated{config, token, tracker}, nil
}

func RetryDelay(normal time.Duration, err error, level uint, now time.Time, jitter float64) time.Duration {
	delay := normal
	var failure *Failure
	if errors.As(err, &failure) && (failure.Code == "network" || failure.Code == "service" || failure.Code == "timeout" || failure.Code == "rate_limit") {
		if level > 4 {
			level = 4
		}
		delay = 5 * time.Second * time.Duration(1<<level)
		if delay > time.Minute {
			delay = time.Minute
		}
		if jitter < 0 {
			jitter = 0
		}
		if jitter > 1 {
			jitter = 1
		}
		delay += time.Duration(float64(delay) * 0.2 * jitter)
		if until := failure.RetryAt.Sub(now); until > delay {
			delay = until
		}
	}
	return delay
}
