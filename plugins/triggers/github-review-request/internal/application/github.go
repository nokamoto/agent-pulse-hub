package application

import (
	"context"
	"fmt"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

type FailureKind string

const (
	FailureAuthentication FailureKind = "authentication"
	FailurePermission     FailureKind = "permission"
	FailureNetwork        FailureKind = "network"
	FailureService        FailureKind = "service"
	FailureRateLimit      FailureKind = "rate-limit"
	FailureUnsupported    FailureKind = "unsupported API"
	FailureIncomplete     FailureKind = "incomplete retrieval"
	FailureCapacity       FailureKind = "capacity"
)

type Failure struct {
	Kind       FailureKind
	StatusCode int
	RetryAt    time.Time
	Reason     string
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	if f.StatusCode != 0 {
		return fmt.Sprintf("%s (HTTP %d)", f.Kind, f.StatusCode)
	}
	return string(f.Kind)
}

type User struct {
	ID    uint64
	Login string
}

type GitHub interface {
	ResolveUser(context.Context, domain.WatchConfig, string) (User, error)
	Observe(context.Context, domain.WatchConfig, string) (map[uint64]string, error)
}

type Credentials interface {
	Token(context.Context, string) (string, error)
}
