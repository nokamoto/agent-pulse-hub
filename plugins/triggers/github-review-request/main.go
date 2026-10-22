package main

import (
	"log/slog"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/adapters/ghcredential"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/adapters/githubapi"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/adapters/githubplugin"
	"google.golang.org/grpc"
)

func main() {
	serve()
}

func serve() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pluginprocess.HandshakeConfig,
		Plugins: plugin.PluginSet{
			pluginprocess.PluginKey(string(domain.InputRole)): &pluginprocess.FrameworkPlugin{
				Impl: githubplugin.New(logger, ghcredential.New(), githubapi.New()),
			},
		},
		GRPCServer: func(options []grpc.ServerOption) *grpc.Server {
			options = append(options, pluginprotocol.ServerOptions()...)
			return grpc.NewServer(options...)
		},
		Logger: hclog.NewNullLogger(),
	})
}
