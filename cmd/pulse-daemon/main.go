package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/codex"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/config"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/controlpipe"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	application "github.com/nokamoto/agent-pulse-hub/internal/application/pulse"
)

func main() {
	logger := newLogger()
	if err := run(logger, os.Args[1:]); err != nil {
		logger.Error("daemon_failed", "reason", sanitize(err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger, arguments []string) error {
	flags := flag.NewFlagSet("pulse-daemon", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "absolute or relative path to the JSON configuration")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("usage: pulse-daemon --config <path>")
	}
	settings, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	ctx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignal()
	delivery, err := codex.New(ctx, settings.CodexExecutable)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(settings.Plugins))
	for _, plugin := range settings.Plugins {
		names = append(names, plugin.Name)
	}
	hub := application.NewHub(names, delivery, logger)
	children := make([]*pluginprocess.Child, 0, len(settings.Plugins))
	for _, definition := range settings.Plugins {
		child, err := pluginprocess.Start(ctx, definition, logger, hub.AcceptEvent, hub.MarkUnavailable)
		if err != nil {
			hub.MarkUnavailable(definition.Name, "startup failed")
			logger.Error("plugin_startup_failed", "plugin", definition.Name, "reason", sanitize(err.Error()))
			continue
		}
		children = append(children, child)
		if err := hub.SetPlugin(definition.Name, child); err != nil {
			child.Stop("plugin exited during startup")
			logger.Error("plugin_startup_failed", "plugin", definition.Name, "reason", sanitize(err.Error()))
		}
	}
	serveReady := make(chan struct{})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- controlpipe.Serve(ctx, logger, func(ctx context.Context, request protocol.ControlRequest) protocol.ControlResponse {
			subscriptionID, err := hub.Register(ctx, request.Plugin, request.SessionID, request.WatchArgs)
			if err != nil {
				var registrationError *application.RegistrationError
				if errors.As(err, &registrationError) {
					return protocol.ControlResponse{Version: protocol.Version, OK: false, Code: registrationError.Code, Message: bounded(registrationError.Message, 512)}
				}
				return protocol.ControlResponse{Version: protocol.Version, OK: false, Code: "internal_error", Message: "registration could not be completed"}
			}
			return protocol.ControlResponse{Version: protocol.Version, OK: true, SubscriptionID: subscriptionID}
		}, func() {
			logger.Info("daemon_ready", "plugin_count", len(settings.Plugins), "available_plugins", countAvailable(children))
			close(serveReady)
		})
	}()
	var serveErr error
	select {
	case <-serveReady:
		serveErr = <-serveDone
	case serveErr = <-serveDone:
	}
	hub.Close()
	stopPlugins(children)
	if serveErr != nil {
		return serveErr
	}
	return nil
}

func stopPlugins(children []*pluginprocess.Child) {
	var wait sync.WaitGroup
	for _, child := range children {
		wait.Add(1)
		go func(child *pluginprocess.Child) {
			defer wait.Done()
			child.Stop("daemon shutdown")
		}(child)
	}
	wait.Wait()
}

func countAvailable(children []*pluginprocess.Child) int {
	count := 0
	for _, child := range children {
		if child.Available() {
			count++
		}
	}
	return count
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: func(_ []string, value slog.Attr) slog.Attr {
		if value.Key == slog.TimeKey {
			if timestamp, ok := value.Value.Any().(time.Time); ok {
				return slog.String(slog.TimeKey, timestamp.UTC().Format(time.RFC3339Nano))
			}
		}
		return value
	}}))
}

func bounded(value string, maximum int) string {
	value = sanitize(value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

func sanitize(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\t' || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	return value
}
