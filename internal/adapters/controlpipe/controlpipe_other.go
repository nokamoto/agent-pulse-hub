//go:build !windows

package controlpipe

import (
	"context"
	"errors"
	"log/slog"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
)

var errWindowsOnly = errors.New("local control pipe is supported only on Windows")

func serve(context.Context, *slog.Logger, Handler, func()) error {
	return errWindowsOnly
}

func register(context.Context, protocol.ControlRequest) (protocol.ControlResponse, error) {
	return protocol.ControlResponse{}, errWindowsOnly
}

func pipeName() (string, error) {
	return "", errWindowsOnly
}
