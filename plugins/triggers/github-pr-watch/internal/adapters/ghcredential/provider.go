package ghcredential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const maxCLIOutput = 8 << 10

type Provider struct {
	lookupEnv func(string) (string, bool)
	environ   func() []string
	lookPath  func(string) (string, error)
	command   func(context.Context, string, ...string) *exec.Cmd
}

func New() *Provider {
	return &Provider{
		lookupEnv: os.LookupEnv,
		environ:   os.Environ,
		lookPath:  exec.LookPath,
		command:   exec.CommandContext,
	}
}

func (p *Provider) Token(ctx context.Context, host string) (string, error) {
	if value, ok := p.lookupEnv("GH_TOKEN"); ok && value != "" {
		return value, nil
	}
	if value, ok := p.lookupEnv("GITHUB_TOKEN"); ok && value != "" {
		return value, nil
	}
	path, err := p.lookPath("gh")
	if err != nil {
		return "", errors.New("GitHub CLI is unavailable")
	}
	command := p.command(ctx, path, "auth", "token", "--hostname", host)
	command.Env = sanitizedEnvironment(p.environ())
	budget := &captureBudget{remaining: maxCLIOutput}
	stdout := &limitedBuffer{limit: maxCLIOutput, budget: budget}
	stderr := &limitedBuffer{limit: maxCLIOutput, budget: budget}
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", errors.New("GitHub CLI process timed out")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("GitHub CLI exited with status %d", exitErr.ExitCode())
		}
		return "", errors.New("GitHub CLI process could not start")
	}
	if stdout.overflow || stderr.overflow {
		return "", errors.New("GitHub CLI output exceeded 8 KiB")
	}
	token := removeTerminalLineEnding(stdout.String())
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", errors.New("GitHub CLI returned an empty or malformed saved token")
	}
	return token, nil
}

func removeTerminalLineEnding(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	if strings.HasSuffix(value, "\n") || strings.HasSuffix(value, "\r") {
		return value[:len(value)-1]
	}
	return value
}

var blockedEnvironment = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_PROMPT_DISABLED",
}

func sanitizedEnvironment(parent []string) []string {
	result := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		key, _, found := strings.Cut(entry, "=")
		if !found || blocked(key) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "GH_PROMPT_DISABLED=1")
}

func blocked(key string) bool {
	for _, candidate := range blockedEnvironment {
		if strings.EqualFold(key, candidate) {
			return true
		}
	}
	return false
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
	budget   *captureBudget
}

type captureBudget struct {
	sync.Mutex
	remaining int
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - b.Len()
	if b.budget != nil {
		b.budget.Lock()
		defer b.budget.Unlock()
		if b.budget.remaining < remaining {
			remaining = b.budget.remaining
		}
	}
	if remaining <= 0 {
		b.overflow = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		if b.budget != nil {
			b.budget.remaining -= remaining
		}
		b.overflow = true
		return len(value), nil
	}
	if b.budget != nil {
		b.budget.remaining -= len(value)
	}
	return b.Buffer.Write(value)
}
