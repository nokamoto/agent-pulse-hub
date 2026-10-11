package main

import (
	"log/slog"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/manualplugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"google.golang.org/grpc"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pluginprocess.HandshakeConfig,
		Plugins: plugin.PluginSet{
			pluginprocess.PluginKey: &pluginprocess.FrameworkPlugin{
				Impl: manualplugin.New(logger),
			},
		},
		GRPCServer: func(options []grpc.ServerOption) *grpc.Server {
			options = append(options, pluginprotocol.ServerOptions()...)
			return grpc.NewServer(options...)
		},
		Logger: hclog.NewNullLogger(),
	})
}
