package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

const WatchValidationTimeout = 8 * time.Second

type ValidatedWatch struct {
	Config  domain.WatchConfig
	Token   string
	User    User
	Tracker *domain.Tracker
}

type WatchValidationError struct {
	Reason     string
	Category   FailureKind
	StatusCode int
	Host       string
}

func (e *WatchValidationError) Error() string { return e.Reason }

// ValidateWatch selects one credential, verifies the user's identity on the
// selected host, then validates restored progress against that identity.
func ValidateWatch(ctx context.Context, args, state *jsonvalue.Value, credentials Credentials, github GitHub) (ValidatedWatch, error) {
	config, err := domain.ParseWatchConfig(args)
	if err != nil {
		return ValidatedWatch{}, &WatchValidationError{Reason: "watch arguments are invalid"}
	}
	validationCtx, cancel := context.WithTimeout(ctx, WatchValidationTimeout)
	defer cancel()
	token, err := credentials.Token(validationCtx, config.Host)
	if err != nil {
		reason := "no usable GitHub credential is available for the selected host"
		switch err.Error() {
		case "GitHub CLI is unavailable":
			reason = "GitHub CLI is unavailable; configure GH_TOKEN, GITHUB_TOKEN, or saved host authentication"
		case "GitHub CLI could not provide a saved token":
			reason = "GitHub CLI could not provide saved authentication for the selected host"
		case "GitHub CLI returned an empty saved token":
			reason = "GitHub CLI returned an empty saved token for the selected host"
		case "GitHub CLI returned an empty or malformed saved token":
			reason = "GitHub CLI returned an empty or malformed saved token for the selected host"
		case "GitHub CLI process timed out":
			reason = "GitHub CLI timed out while retrieving saved authentication"
		case "GitHub CLI process could not start":
			reason = "GitHub CLI could not start while retrieving saved authentication"
		case "GitHub CLI output exceeded 64 KiB":
			reason = "GitHub CLI output exceeded the 64 KiB limit"
		default:
			if strings.HasPrefix(err.Error(), "GitHub CLI exited with status ") {
				reason = err.Error()
			}
		}
		return ValidatedWatch{}, &WatchValidationError{Reason: reason, Category: FailureAuthentication, Host: config.Host}
	}
	user, err := github.ResolveUser(validationCtx, config, token)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			return ValidatedWatch{}, &WatchValidationError{Reason: "GitHub identity verification failed", Category: failure.Kind, StatusCode: failure.StatusCode, Host: config.Host}
		}
		return ValidatedWatch{}, &WatchValidationError{Reason: "GitHub identity verification failed", Category: FailureNetwork, Host: config.Host}
	}
	if user.ID == 0 || strings.TrimSpace(user.Login) == "" {
		return ValidatedWatch{}, &WatchValidationError{Reason: "GitHub returned an invalid authenticated identity", Category: FailureIncomplete, Host: config.Host}
	}
	var tracker *domain.Tracker
	if state == nil || state.Kind() == jsonvalue.Null {
		tracker = domain.NewTracker(config.Host, user.ID)
	} else {
		tracker, err = domain.RestoreTracker(state, config.Host, user.ID)
		if err != nil {
			return ValidatedWatch{}, &WatchValidationError{Reason: "saved GitHub review progress is incompatible with this host or authenticated user", Host: config.Host}
		}
	}
	return ValidatedWatch{Config: config, Token: token, User: user, Tracker: tracker}, nil
}
