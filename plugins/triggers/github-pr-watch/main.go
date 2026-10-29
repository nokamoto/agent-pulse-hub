package main

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/adapters/ghcredential"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/adapters/githubapi"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/adapters/githubplugin"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
	"google.golang.org/grpc"
)

func main() {
	if !serveWithOptions(nil) {
		os.Exit(2)
	}
}

func serveWithOptions(options []githubplugin.Option, serverOptions ...grpc.ServerOption) bool {
	flags := flag.NewFlagSet("github-pr-watch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	host := flags.String("host", "github.com", "GitHub DNS hostname")
	interval := flags.Int("poll-interval", 60, "poll interval in seconds")
	observationBytes := flags.String("observation-byte-budget", "67108864", "complete observation decoded-byte budget")
	historyBytes := flags.String("history-byte-budget", "67108864", "retained journal byte budget")
	profile := flags.String("state-profile", "signals-v3", "signals-v3 or discussion-v2 state profile")
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 || !domain.ValidHost(*host) || *interval < 5 || *interval > 3600 {
		logger.Error("invalid_plugin_configuration")
		return false
	}
	observationBudget, observationErr := domain.ParseByteBudget(*observationBytes)
	historyBudget, historyErr := domain.ParseByteBudget(*historyBytes)
	if observationErr != nil || historyErr != nil || (*profile != "signals-v3" && *profile != "discussion-v2") {
		logger.Error("invalid_plugin_configuration")
		return false
	}
	options = append(options, githubplugin.WithByteBudgets(observationBudget, historyBudget))
	options = append(options, githubplugin.WithSignals(*profile == "signals-v3"))
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pluginprocess.HandshakeConfig,
		Plugins:         plugin.PluginSet{pluginprocess.InputPluginKey: &pluginprocess.FrameworkPlugin{Impl: githubplugin.New(logger, ghcredential.New(), githubapi.New().WithByteBudget(observationBudget).WithSignals(*profile == "signals-v3"), *host, time.Duration(*interval)*time.Second, options...)}},
		GRPCServer: func(base []grpc.ServerOption) *grpc.Server {
			base = append(base, pluginprotocol.ServerOptions()...)
			base = append(base, serverOptions...)
			return grpc.NewServer(base...)
		},
		Logger: hclog.NewNullLogger(),
	})
	return true
}
