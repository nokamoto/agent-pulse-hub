package application

import "context"

const MaxCapturedBytes = 64 * 1024

type ProcessResult struct {
	Started        bool
	Exited         bool
	ExitCode       int
	Stdout         []byte
	Stderr         []byte
	StdoutOverflow bool
	StderrOverflow bool
	Err            error
}

type ProcessRunner interface {
	Run(context.Context, string, []string) ProcessResult
}

//go:generate go run go.uber.org/mock/mockgen -source=process.go -destination=process_mock_test.go -package=application
