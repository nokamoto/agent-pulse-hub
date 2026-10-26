package ghcredential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const maxCLIOutput = 64 << 10

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
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := p.command(commandCtx, path, "auth", "token", "--hostname", host)
	command.Env = sanitizedEnvironment(p.environ())
	stdout := &limitedBuffer{limit: maxCLIOutput}
	stderr := &limitedBuffer{limit: maxCLIOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		if commandCtx.Err() != nil {
			return "", errors.New("GitHub CLI process timed out")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("GitHub CLI exited with status %d", exitErr.ExitCode())
		}
		return "", errors.New("GitHub CLI process could not start")
	}
	if stdout.overflow || stderr.overflow {
		return "", errors.New("GitHub CLI output exceeded 64 KiB")
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
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = b.Buffer.Write(value[:remaining])
		b.overflow = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}
