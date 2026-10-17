package ghcredential

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("APH_GH_CREDENTIAL_HELPER") == "1" {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "-test.run=TestCredentialHelper" {
			args = args[1:]
		}
		journal := os.Getenv("APH_GH_CREDENTIAL_JOURNAL")
		blockedValues := make(map[string]string)
		for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
			blockedValues[key], _ = os.LookupEnv(key)
		}
		data, _ := json.Marshal(map[string]any{
			"args": args, "blocked": blockedValues, "prompt_disabled": os.Getenv("GH_PROMPT_DISABLED"),
		})
		_ = os.WriteFile(journal, data, 0o600)
		_, _ = os.Stdout.Write([]byte("saved-token\r\n"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestTokenPrefersEnvironmentWithoutLookingUpCLI(t *testing.T) {
	lookedUp := false
	provider := &Provider{
		lookupEnv: func(key string) (string, bool) {
			switch key {
			case "GH_TOKEN":
				return "", true
			case "GITHUB_TOKEN":
				return "selected", true
			default:
				return "", false
			}
		},
		lookPath: func(string) (string, error) { lookedUp = true; return "", nil },
	}
	token, err := provider.Token(context.Background(), "ghe.example.test")
	if err != nil || token != "selected" || lookedUp {
		t.Fatalf("token=%q error=%v CLI looked up=%v", token, err, lookedUp)
	}
}

func TestSavedCLICredentialArgumentsAndEnvironment(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "child.json")
	var executable string
	var capturedName string
	var capturedArgs []string
	provider := &Provider{
		lookupEnv: func(string) (string, bool) { return "", false },
		environ: func() []string {
			return []string{
				"PATH=isolated",
				"GH_TOKEN=poison",
				"GITHUB_TOKEN=poison",
				"GH_ENTERPRISE_TOKEN=poison",
				"GITHUB_ENTERPRISE_TOKEN=poison",
				"GH_HOST=wrong.example.test",
				"APH_GH_CREDENTIAL_HELPER=1",
				"APH_GH_CREDENTIAL_JOURNAL=" + journal,
			}
		},
		lookPath: func(name string) (string, error) { return name, nil },
		command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedName = name
			capturedArgs = append([]string(nil), args...)
			commandArgs := append([]string{"-test.run=TestCredentialHelper"}, args...)
			executable = os.Args[0]
			return exec.CommandContext(ctx, executable, commandArgs...)
		},
	}
	token, err := provider.Token(context.Background(), "ghe.example.test")
	if err != nil || token != "saved-token" {
		t.Fatalf("token=%q error=%v", token, err)
	}
	if capturedName != "gh" || !reflect.DeepEqual(capturedArgs, []string{"auth", "token", "--hostname", "ghe.example.test"}) {
		t.Fatalf("command=%q args=%q", capturedName, capturedArgs)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	var child struct {
		Args           []string          `json:"args"`
		Blocked        map[string]string `json:"blocked"`
		PromptDisabled string            `json:"prompt_disabled"`
	}
	if err := json.Unmarshal(data, &child); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(child.Args, capturedArgs) || child.PromptDisabled != "1" {
		t.Fatalf("child args=%q prompt=%q", child.Args, child.PromptDisabled)
	}
	for key, value := range child.Blocked {
		if value != "" {
			t.Errorf("child inherited %s=%q", key, value)
		}
	}
}

func TestLimitedBufferStopsRetainingAfterLimit(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("123456")); err != nil || n != 6 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if buffer.String() != "1234" || !buffer.overflow {
		t.Fatalf("buffer=%q overflow=%v", buffer.String(), buffer.overflow)
	}
}
