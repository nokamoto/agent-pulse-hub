package ghcredential

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEnvironmentSelectionNeverLooksUpCLI(t *testing.T) {
	for _, test := range []struct{ name, gh, github, want string }{{"first", "first", "second", "first"}, {"second", "", "second", "second"}} {
		t.Run(test.name, func(t *testing.T) {
			p := &Provider{lookupEnv: func(key string) (string, bool) {
				if key == "GH_TOKEN" {
					return test.gh, true
				}
				return test.github, true
			}, lookPath: func(string) (string, error) {
				t.Fatal("CLI consulted for environment token")
				return "", errors.New("unexpected")
			}}
			token, err := p.Token(context.Background(), "github.com")
			if err != nil || token != test.want {
				t.Fatalf("token=%q err=%v", token, err)
			}
		})
	}
}

func TestSharedCaptureBudget(t *testing.T) {
	budget := &captureBudget{remaining: maxCLIOutput}
	stdout := &limitedBuffer{limit: maxCLIOutput, budget: budget}
	stderr := &limitedBuffer{limit: maxCLIOutput, budget: budget}
	_, _ = stdout.Write([]byte(strings.Repeat("a", 6000)))
	_, _ = stderr.Write([]byte(strings.Repeat("b", 6000)))
	if stdout.Len()+stderr.Len() != maxCLIOutput || !stderr.overflow {
		t.Fatalf("captured=%d+%d overflow=%v", stdout.Len(), stderr.Len(), stderr.overflow)
	}
}

func TestSanitizedCLIEnvironment(t *testing.T) {
	got := sanitizedEnvironment([]string{"PATH=/bin", "GH_TOKEN=secret", "github_token=secret", "GH_ENTERPRISE_TOKEN=secret", "GITHUB_ENTERPRISE_TOKEN=secret", "GH_HOST=wrong", "GH_PROMPT_DISABLED=0", "OTHER=kept"})
	want := []string{"PATH=/bin", "OTHER=kept", "GH_PROMPT_DISABLED=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%v", got)
	}
}

func TestCLIOutputLineEnding(t *testing.T) {
	for _, input := range []string{"token\n", "token\r\n", "token\r", "token"} {
		if got := removeTerminalLineEnding(input); got != "token" {
			t.Fatalf("trim %q=%q", input, got)
		}
	}
}
