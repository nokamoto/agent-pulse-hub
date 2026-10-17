package application

import (
	"context"
	"errors"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

type testCredentials struct {
	token string
	err   error
}

func (c testCredentials) Token(context.Context, string) (string, error) { return c.token, c.err }

type testGitHub struct {
	user  User
	err   error
	host  string
	token string
}

func (g *testGitHub) ResolveUser(_ context.Context, config domain.WatchConfig, token string) (User, error) {
	g.host, g.token = config.Host, token
	return g.user, g.err
}

func (g *testGitHub) Observe(context.Context, domain.WatchConfig, string) (map[uint64]string, error) {
	return nil, errors.New("not used")
}

func TestValidateWatchSelectsIdentityAndRestoresState(t *testing.T) {
	github := &testGitHub{user: User{ID: 42, Login: "operator"}}
	args := mustJSON(t, `{"host":"GITHUB.com","poll_interval":"5s"}`)
	validated, err := ValidateWatch(context.Background(), args, jsonvalue.NewNull(), testCredentials{token: "marker-token"}, github)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Config.Host != "github.com" || validated.Config.PollInterval.String() != "5s" || validated.User.ID != 42 || validated.Token != "marker-token" {
		t.Fatalf("unexpected validated watch: %#v", validated)
	}
	if github.host != "github.com" || github.token != "marker-token" {
		t.Fatalf("identity request host=%q token=%q", github.host, github.token)
	}
	state, err := validated.Tracker.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWatch(context.Background(), args, state, testCredentials{token: "marker-token"}, github); err != nil {
		t.Fatalf("compatible state was rejected: %v", err)
	}
}

func TestValidateWatchRejectsCredentialIdentityAndCheckpointFailures(t *testing.T) {
	args := mustJSON(t, `{}`)
	github := &testGitHub{user: User{ID: 42, Login: "operator"}}
	if _, err := ValidateWatch(context.Background(), args, jsonvalue.NewNull(), testCredentials{err: errors.New("unavailable")}, github); err == nil {
		t.Fatal("accepted missing credentials")
	}
	github.err = errors.New("rejected token")
	if _, err := ValidateWatch(context.Background(), args, jsonvalue.NewNull(), testCredentials{token: "bad"}, github); err == nil {
		t.Fatal("accepted rejected credential")
	}
	github.err = nil
	github.user = User{ID: 0, Login: "operator"}
	if _, err := ValidateWatch(context.Background(), args, jsonvalue.NewNull(), testCredentials{token: "bad"}, github); err == nil {
		t.Fatal("accepted invalid user identity")
	}
	github.user = User{ID: 43, Login: "other"}
	state := mustJSON(t, `{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"42","entries":[]}}`)
	if _, err := ValidateWatch(context.Background(), args, state, testCredentials{token: "good"}, github); err == nil {
		t.Fatal("accepted checkpoint for another authenticated user")
	}
}

func mustJSON(t *testing.T, text string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(text), 32_768)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
