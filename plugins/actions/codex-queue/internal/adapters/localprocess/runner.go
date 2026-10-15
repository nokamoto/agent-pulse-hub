package localprocess

import (
	"bytes"
	"context"
	"os/exec"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/application"
)

const PipeWaitDelay = time.Second

type Runner struct{}

var _ application.ProcessRunner = Runner{}

func (Runner) Run(ctx context.Context, executable string, arguments []string) application.ProcessResult {
	stdout := newCappedBuffer(application.MaxCapturedBytes)
	stderr := newCappedBuffer(application.MaxCapturedBytes)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.WaitDelay = PipeWaitDelay
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return application.ProcessResult{ExitCode: -1, Err: err}
	}
	err := command.Wait()
	result := application.ProcessResult{
		Started:        true,
		Exited:         command.ProcessState != nil,
		ExitCode:       -1,
		Stdout:         stdout.Bytes(),
		Stderr:         stderr.Bytes(),
		StdoutOverflow: stdout.Overflowed(),
		StderrOverflow: stderr.Overflowed(),
		Err:            err,
	}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	return result
}

type cappedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func (b *cappedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if remaining > len(value) {
			remaining = len(value)
		}
		_, _ = b.buffer.Write(value[:remaining])
	}
	if remaining < len(value) {
		b.overflow = true
	}
	return len(value), nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func (b *cappedBuffer) Overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}
