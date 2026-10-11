//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/codex"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/config"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/controlpipe"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

//go:generate go run go.uber.org/mock/mockgen -source=main_windows.go -destination=command_mock_windows_test.go -package=main
type commandOperations interface {
	Daemon(string, io.Writer) error
	LookupEnv(string) (string, bool)
	WatchArguments(string) (*jsonvalue.Value, error)
	Register(context.Context, string, string, *jsonvalue.Value) (controlpipe.RegistrationResponse, []byte, error)
}

type localOperations struct{}

func (localOperations) Daemon(path string, stderr io.Writer) error { return runDaemon(path, stderr) }
func (localOperations) LookupEnv(key string) (string, bool)        { return os.LookupEnv(key) }
func (localOperations) WatchArguments(path string) (*jsonvalue.Value, error) {
	return loadWatchArguments(path)
}

func (localOperations) Register(ctx context.Context, plugin, session string, args *jsonvalue.Value) (controlpipe.RegistrationResponse, []byte, error) {
	return controlpipe.Register(ctx, plugin, session, args)
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, localOperations{})
}

func runWith(args []string, stdout, stderr io.Writer, operations commandOperations) int {
	if len(args) == 0 {
		return report(stderr, "usage: agent-pulse-hub <daemon|register> ...")
	}
	switch args[0] {
	case "daemon":
		flags, err := parseFlags(args[1:], "--config")
		if err != nil {
			return report(stderr, "daemon: "+err.Error())
		}
		if err := operations.Daemon(flags["--config"], stderr); err != nil {
			return report(stderr, "daemon: "+err.Error())
		}
		return 0
	case "register":
		flags, err := parseFlags(args[1:], "--plugin", "--session-id", "--watch-args-file")
		if err != nil {
			return report(stderr, "register: "+err.Error())
		}
		threadID, err := sessionIDFromEnvironment(operations.LookupEnv)
		if err != nil {
			return report(stderr, "register: "+err.Error())
		}
		if !strings.EqualFold(flags["--session-id"], threadID) {
			return report(stderr, "register: --session-id must match the current Codex conversation.")
		}
		if flags["--plugin"] == "" {
			return report(stderr, "register: --plugin must be nonempty.")
		}
		watchArgs, err := operations.WatchArguments(flags["--watch-args-file"])
		if err != nil {
			return report(stderr, "register: "+err.Error())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, wire, err := operations.Register(ctx, flags["--plugin"], threadID, watchArgs)
		if err != nil {
			var daemonError *controlpipe.DaemonError
			if errors.As(err, &daemonError) {
				if writeErr := writeAll(stdout, daemonError.Wire); writeErr != nil {
					return report(stderr, "register: could not write the daemon response to stdout.")
				}
				return 1
			}
			var uncertain *controlpipe.UncertainError
			if errors.As(err, &uncertain) {
				return report(stderr, uncertain.Error())
			}
			return report(stderr, err.Error())
		}
		if !response.Success {
			return report(stderr, "register: daemon returned an invalid result.")
		}
		if err := writeAll(stdout, wire); err != nil {
			return report(stderr, "register: registration succeeded but its response could not be written to stdout.")
		}
		return 0
	default:
		return report(stderr, "usage: agent-pulse-hub <daemon|register> ...")
	}
}

func runDaemon(configPath string, stderr io.Writer) error {
	settings, err := config.Load(configPath)
	if err != nil {
		return err
	}
	logger := daemonLogger(stderr)
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 6*time.Second)
	version, err := codex.ReadVersion(probeCtx, settings.CodexExecutable)
	probeCancel()
	if err != nil {
		return err
	}
	logger.Info("codex_cli_version", "version", version)
	service := hub.New(codex.New(settings.CodexExecutable), logger)
	for _, configured := range settings.Plugins {
		process := pluginprocess.New(pluginprocess.Config{
			Name: configured.Name, Executable: configured.Executable, Args: configured.Args,
		}, logger)
		if err := service.AddPlugin(configured.Name, process); err != nil {
			service.Shutdown()
			return err
		}
	}

	server, err := controlpipe.NewServer(service, logger)
	if err != nil {
		service.Shutdown()
		return err
	}
	ctx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignal()
	service.StartPlugins(ctx)
	logger.Info("registration_available", "available", true)
	serveErr := server.Serve(ctx)
	_ = server.Close()
	service.Shutdown()
	stopCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	service.StopPlugins(stopCtx)
	if serveErr != nil {
		return serveErr
	}
	return nil
}

func daemonLogger(stderr io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
			if attribute.Key == slog.TimeKey && attribute.Value.Kind() == slog.KindTime {
				attribute.Value = slog.TimeValue(attribute.Value.Time().UTC())
			}
			return attribute
		},
	}))
}

func parseFlags(args []string, names ...string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(names))
	values := make(map[string]string, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	for index := 0; index < len(args); {
		name := args[index]
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("unknown or positional argument %q", name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("flag %s may be supplied only once", name)
		}
		index++
		if index >= len(args) || args[index] == "" || strings.HasPrefix(args[index], "--") {
			return nil, fmt.Errorf("flag %s requires one value", name)
		}
		values[name] = args[index]
		index++
	}
	for _, name := range names {
		if _, ok := values[name]; !ok {
			return nil, fmt.Errorf("required flag %s is missing", name)
		}
	}
	return values, nil
}

func sessionIDFromEnvironment(lookup func(string) (string, bool)) (string, error) {
	threadID, threadOK := lookup("CODEX_THREAD_ID")
	sessionID, sessionOK := lookup("CODEX_SESSION_ID")
	if !threadOK || !sessionOK || !domain.IsUUID(threadID) || !domain.IsUUID(sessionID) || !strings.EqualFold(threadID, sessionID) {
		return "", errors.New("CODEX_THREAD_ID and CODEX_SESSION_ID must both be matching UUIDs")
	}
	return threadID, nil
}

func loadWatchArguments(path string) (*jsonvalue.Value, error) {
	return readWatchArguments(path, localWatchFiles{})
}

type watchFiles interface {
	Stat(string) (os.FileInfo, error)
	Open(string) (io.ReadCloser, error)
}

type localWatchFiles struct{}

func (localWatchFiles) Stat(path string) (os.FileInfo, error)   { return os.Stat(path) }
func (localWatchFiles) Open(path string) (io.ReadCloser, error) { return os.Open(path) }

func readWatchArguments(path string, files watchFiles) (*jsonvalue.Value, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("watch arguments path must be absolute")
	}
	info, err := files.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open watch arguments file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("watch arguments path must name a regular file")
	}
	if info.Size() >= protocol.MaxFrameBytes {
		return nil, protocol.ErrFrameTooLarge
	}
	file, err := files.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open watch arguments file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxFrameBytes))
	if err != nil {
		return nil, fmt.Errorf("read watch arguments file: %w", err)
	}
	if len(data) >= protocol.MaxFrameBytes {
		return nil, protocol.ErrFrameTooLarge
	}
	value, err := jsonvalue.Parse(data, protocol.MaxFrameBytes-1)
	if err != nil {
		return nil, fmt.Errorf("parse watch arguments JSON: %w", err)
	}
	if value.Kind() != jsonvalue.Object {
		return nil, errors.New("watch arguments file must contain a JSON object")
	}
	return value, nil
}

func report(writer io.Writer, message string) int {
	_, _ = fmt.Fprintln(writer, boundedDiagnostic(message, 1_024))
	return 1
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func boundedDiagnostic(value string, max int) string {
	var result strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			character = ' '
		}
		encoded := string(character)
		if result.Len()+len(encoded) > max {
			break
		}
		result.WriteString(encoded)
	}
	if !utf8.ValidString(result.String()) {
		return "operation failed"
	}
	return result.String()
}
