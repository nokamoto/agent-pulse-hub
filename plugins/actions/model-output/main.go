package main

import (
	"log/slog"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/model-output/internal/adapters/modeloutput"
	"google.golang.org/grpc"
)

func main() {
	enabled := false
	for _, a := range os.Args[1:] {
		if a != "--notification-delivery" || enabled {
			os.Exit(1)
		}
		enabled = true
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pluginprocess.HandshakeConfig,
		Plugins: plugin.PluginSet{
			pluginprocess.PluginKey(string(domain.OutputRole)): &pluginprocess.FrameworkPlugin{
				Impl: modeloutput.New(logger).WithNotifications(enabled),
			},
		},
		GRPCServer: func(options []grpc.ServerOption) *grpc.Server {
			options = append(options, pluginprotocol.ServerOptions()...)
			return grpc.NewServer(options...)
		},
		Logger: hclog.NewNullLogger(),
	})
}
