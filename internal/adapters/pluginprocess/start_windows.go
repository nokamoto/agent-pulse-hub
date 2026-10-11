//go:build windows

package pluginprocess

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/windowsprocess"
)

type (
	systemLauncher struct{}
	windowsChild   struct {
		command               *windowsprocess.Command
		stdin, stdout, stderr *os.File
	}
)

func (systemLauncher) Start(config Config) (childProcess, error) {
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create plugin stdin pipe: %w", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		closeFiles(stdinRead, stdinWrite)
		return nil, fmt.Errorf("create plugin stdout pipe: %w", err)
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		closeFiles(stdinRead, stdinWrite, stdoutRead, stdoutWrite)
		return nil, fmt.Errorf("create plugin stderr pipe: %w", err)
	}
	command, err := windowsprocess.Start(context.Background(), config.Executable, config.Args, stdinRead, stdoutWrite, stderrWrite)
	closeFiles(stdinRead, stdoutWrite, stderrWrite)
	if err != nil {
		closeFiles(stdinWrite, stdoutRead, stderrRead)
		return nil, err
	}
	return &windowsChild{command: command, stdin: stdinWrite, stdout: stdoutRead, stderr: stderrRead}, nil
}

func (p *windowsChild) Stdin() io.WriteCloser { return p.stdin }
func (p *windowsChild) Stdout() io.ReadCloser { return p.stdout }
func (p *windowsChild) Stderr() io.ReadCloser { return p.stderr }
func (p *windowsChild) Wait() error           { return p.command.Wait() }
func (p *windowsChild) Terminate()            { p.command.Terminate() }
func (p *windowsChild) Close()                { p.command.Close(); closeFiles(p.stdin, p.stdout, p.stderr) }

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
