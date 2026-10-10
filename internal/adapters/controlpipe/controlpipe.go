package controlpipe

import (
	"context"
	"log/slog"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
)

type Handler func(context.Context, protocol.ControlRequest) protocol.ControlResponse

func Serve(ctx context.Context, logger *slog.Logger, handler Handler, onReady func()) error {
	return serve(ctx, logger, handler, onReady)
}

func Register(ctx context.Context, request protocol.ControlRequest) (protocol.ControlResponse, error) {
	return register(ctx, request)
}

func PipeName() (string, error) {
	return pipeName()
}
