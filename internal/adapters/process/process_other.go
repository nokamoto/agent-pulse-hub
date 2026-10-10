//go:build !windows

package process

import (
	"os"
	"os/exec"
)

type Job struct{}

func NewJob() (*Job, error) {
	return &Job{}, nil
}

func Start(command *exec.Cmd, _ *Job) error {
	return command.Start()
}

func (job *Job) Close() error {
	return nil
}

func WaitForExit(_ *os.Process) <-chan struct{} {
	return nil
}
