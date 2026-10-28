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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/config"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/control"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/registrationstore"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type commandOperations interface {
	Daemon(configPath, stateDirectory, address string, stderr io.Writer) error
	Arguments(path string) (*jsonvalue.Value, error)
	Register(context.Context, string, string, *jsonvalue.Value, string, *jsonvalue.Value) (control.RegistrationResponse, []byte, error)
	Stop(context.Context, string) error
}

type localOperations struct{}

type rewaitOperations interface {
	Position(string) (string, error)
	Rewait(context.Context, string, string, string, string) (control.RegistrationResponse, []byte, error)
}

func (localOperations) Position(path string) (string, error) {
	value, err := loadJSONFile(path)
	if err != nil {
		return "", errors.New("confirmation position file must be an accessible absolute regular file containing strict UTF-8 JSON without a BOM")
	}
	position, ok := jsonvalue.ParseString(value)
	if !ok || !control.ValidPosition(position) {
		return "", errors.New("confirmation position file must contain one nonempty JSON string of at most 4096 UTF-8 bytes")
	}
	return position, nil
}

func (localOperations) Rewait(ctx context.Context, address, subscriptionID, operationID, position string) (control.RegistrationResponse, []byte, error) {
	return control.Rewait(ctx, address, subscriptionID, operationID, position)
}

func (localOperations) Daemon(path, stateDirectory, address string, stderr io.Writer) error {
	return runDaemon(path, stateDirectory, address, stderr)
}

func (localOperations) DaemonWithRuntimeBudget(path, stateDirectory, address string, budget uint64, stderr io.Writer) error {
	return runDaemonWithRuntimeBudget(path, stateDirectory, address, budget, stderr)
}

func (localOperations) Arguments(path string) (*jsonvalue.Value, error) { return loadArguments(path) }

func (localOperations) Register(ctx context.Context, address, inputName string, watchArgs *jsonvalue.Value, outputName string, destinationArgs *jsonvalue.Value) (control.RegistrationResponse, []byte, error) {
	return control.Register(ctx, address, inputName, watchArgs, outputName, destinationArgs)
}

func (localOperations) Stop(ctx context.Context, address string) error {
	return control.Stop(ctx, address)
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, localOperations{})
}

func runWith(args []string, stdout, stderr io.Writer, operations commandOperations) int {
	if len(args) == 0 {
		return report(stderr, "usage: agent-pulse-hub <daemon|register|rewait|stop> ...")
	}
	switch args[0] {
	case "daemon":
		flags, err := parseFlags(args[1:], []string{"--config"}, []string{"--state-dir", "--address", "--runtime-state-max-bytes"})
		if err != nil {
			return report(stderr, "daemon: "+err.Error())
		}
		budget := hub.DefaultRuntimeStateMaxBytes
		if value, exists := flags["--runtime-state-max-bytes"]; exists {
			var err error
			budget, err = parseRuntimeByteBudget(value)
			if err != nil {
				return report(stderr, "daemon: "+err.Error())
			}
		}
		var runErr error
		if op, ok := operations.(interface {
			DaemonWithRuntimeBudget(string, string, string, uint64, io.Writer) error
		}); ok {
			runErr = op.DaemonWithRuntimeBudget(flags["--config"], flags["--state-dir"], addressOrDefault(flags["--address"]), budget, stderr)
		} else if budget != hub.DefaultRuntimeStateMaxBytes {
			return report(stderr, "daemon: runtime-state byte configuration unavailable")
		} else {
			runErr = operations.Daemon(flags["--config"], flags["--state-dir"], addressOrDefault(flags["--address"]), stderr)
		}
		if err := runErr; err != nil {
			return report(stderr, "daemon: "+err.Error())
		}
		return 0
	case "register":
		flags, err := parseFlags(args[1:], []string{"--input-plugin", "--watch-args-file", "--output-plugin", "--destination-args-file"}, []string{"--address"})
		if err != nil {
			return report(stderr, "register: "+err.Error())
		}
		watchArgs, err := operations.Arguments(flags["--watch-args-file"])
		if err != nil {
			return report(stderr, "register: "+err.Error())
		}
		destinationArgs, err := operations.Arguments(flags["--destination-args-file"])
		if err != nil {
			return report(stderr, "register: "+err.Error())
		}
		ctx, cancel := context.WithTimeout(context.Background(), controlDeadline)
		defer cancel()
		_, wire, err := operations.Register(ctx, addressOrDefault(flags["--address"]), flags["--input-plugin"], watchArgs, flags["--output-plugin"], destinationArgs)
		if err != nil {
			var daemonError *control.DaemonError
			if errors.As(err, &daemonError) {
				if writeErr := writeAll(stdout, daemonError.Wire); writeErr != nil {
					return report(stderr, "register: could not write the daemon response to stdout.")
				}
				return 1
			}
			return report(stderr, "register: "+err.Error())
		}
		if err := writeAll(stdout, wire); err != nil {
			return report(stderr, "register: registration succeeded but its response could not be written to stdout.")
		}
		return 0
	case "rewait":
		flags, err := parseFlags(args[1:], []string{"--subscription-id", "--operation-id", "--confirmation-position-file"}, []string{"--address"})
		if err != nil {
			return report(stderr, "rewait: "+err.Error())
		}
		if !control.ValidOperationID(flags["--operation-id"]) {
			return report(stderr, "rewait: operation ID must be canonical Unix milliseconds, a colon, and 32 lowercase hexadecimal digits")
		}
		rewait, ok := operations.(rewaitOperations)
		if !ok {
			return report(stderr, "rewait: command operations are unavailable")
		}
		position, err := rewait.Position(flags["--confirmation-position-file"])
		if err != nil {
			return report(stderr, "rewait: "+err.Error())
		}
		if !control.ValidPosition(position) {
			return report(stderr, "rewait: confirmation position is invalid")
		}
		ctx, cancel := context.WithTimeout(context.Background(), controlDeadline)
		defer cancel()
		_, wire, err := rewait.Rewait(ctx, addressOrDefault(flags["--address"]), flags["--subscription-id"], flags["--operation-id"], position)
		if err != nil {
			var daemonError *control.DaemonError
			if errors.As(err, &daemonError) {
				if writeAll(stdout, daemonError.Wire) != nil {
					return report(stderr, "rewait: could not write the daemon response to stdout.")
				}
				return 1
			}
			return report(stderr, "rewait: "+err.Error())
		}
		if writeAll(stdout, wire) != nil {
			return report(stderr, "rewait: wait was installed but its response could not be written to stdout.")
		}
		return 0
	case "stop":
		flags, err := parseFlags(args[1:], nil, []string{"--address"})
		if err != nil {
			return report(stderr, "stop: "+err.Error())
		}
		ctx, cancel := context.WithTimeout(context.Background(), controlDeadline)
		defer cancel()
		if err := operations.Stop(ctx, addressOrDefault(flags["--address"])); err != nil {
			return report(stderr, "stop: "+err.Error())
		}
		if err := writeAll(stdout, []byte("{\"ok\":true}\n")); err != nil {
			return report(stderr, "stop succeeded but its response could not be written to stdout.")
		}
		return 0
	default:
		return report(stderr, "usage: agent-pulse-hub <daemon|register|rewait|stop> ...")
	}
}

const controlDeadline = 25 * time.Second

func addressOrDefault(address string) string {
	if address == "" {
		return control.DefaultAddress
	}
	return address
}

func runDaemon(configPath, selectedStateDirectory, address string, stderr io.Writer) (returnErr error) {
	return runDaemonWithRuntimeBudget(configPath, selectedStateDirectory, address, hub.DefaultRuntimeStateMaxBytes, stderr)
}

func parseRuntimeByteBudget(value string) (uint64, error) {
	if value == "" {
		return 0, errors.New("runtime-state-max-bytes must be positive decimal bytes")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, errors.New("runtime-state-max-bytes must be positive decimal bytes")
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("runtime-state-max-bytes must be positive decimal bytes through uint64 maximum")
	}
	return n, nil
}

func runDaemonWithRuntimeBudget(configPath, selectedStateDirectory, address string, budget uint64, stderr io.Writer) (returnErr error) {
	settings, err := config.Load(configPath)
	if err != nil {
		return err
	}
	stateDirectory, err := resolveStateDirectory(configPath, selectedStateDirectory)
	if err != nil {
		return err
	}
	store, err := registrationstore.Open(stateDirectory)
	if err != nil {
		return err
	}
	registrations, err := store.Load()
	if err != nil {
		return errors.Join(err, store.Close())
	}
	logger := daemonLogger(stderr)
	service := hub.NewWithRegistrationStore(logger, store)
	if err := service.SetRuntimeStateMaxBytes(budget); err != nil {
		return errors.Join(err, store.Close())
	}
	var server *control.Server
	defer func() {
		if server != nil {
			if closeErr := server.Close(); closeErr != nil {
				returnErr = errors.Join(returnErr, closeErr)
			}
		}
		service.Shutdown()
		service.StopPlugins(context.Background())
		if closeErr := store.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	for _, configured := range settings.Plugins {
		process := pluginprocess.New(pluginprocess.Config{
			RuntimeStateMaxBytes: budget,
			Name:                 configured.Name, Role: configured.Role, Executable: configured.Executable, Args: configured.Args,
		}, logger)
		if err := service.AddPlugin(configured.Name, configured.Role, process); err != nil {
			return err
		}
	}
	if err := service.PrepareRestorations(registrations); err != nil {
		return err
	}
	server, err = control.NewServer(address, service, logger)
	if err != nil {
		return err
	}

	ctx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignal()
	serveErr := runDaemonLifecycle(ctx, func(ctx context.Context) error {
		service.StartPlugins(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err := service.Restore(ctx); err != nil {
			return err
		}
		service.StartRuntimeCheckpoints()
		logger.Info("registration_available", "available", true)
		return nil
	}, server.Serve, func() {
		service.Shutdown()
		service.StopPlugins(context.Background())
	})
	if serveErr != nil {
		return serveErr
	}
	return nil
}

func runDaemonLifecycle(ctx context.Context, start func(context.Context) error, serve func(context.Context) error, shutdown func()) error {
	if err := start(ctx); err != nil {
		shutdown()
		return err
	}
	serveErr := serve(ctx)
	shutdown()
	return serveErr
}

func resolveStateDirectory(configPath, selected string) (string, error) {
	if selected == "" {
		if !filepath.IsAbs(configPath) {
			return "", errors.New("configuration path must be absolute")
		}
		return filepath.Clean(configPath) + ".state", nil
	}
	if !filepath.IsAbs(selected) {
		return "", errors.New("state directory path must be absolute")
	}
	return filepath.Clean(selected), nil
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

func parseFlags(args, required, optional []string) (map[string]string, error) {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	values := make(map[string]string, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
	}
	for _, name := range optional {
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
	for _, name := range required {
		if _, ok := values[name]; !ok {
			return nil, fmt.Errorf("required flag %s is missing", name)
		}
	}
	return values, nil
}

func loadArguments(path string) (*jsonvalue.Value, error) {
	value, err := loadJSONFile(path)
	if err != nil {
		return nil, err
	}
	if value.Kind() != jsonvalue.Object {
		return nil, errors.New("argument file must contain a JSON object")
	}
	return value, nil
}

func loadJSONFile(path string) (*jsonvalue.Value, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("argument file path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open argument file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("argument file path must name a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open argument file: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxFrameBytes))
	if err != nil {
		return nil, fmt.Errorf("read argument file: %w", err)
	}
	if len(data) >= protocol.MaxFrameBytes {
		return nil, protocol.ErrFrameTooLarge
	}
	value, err := jsonvalue.Parse(data, protocol.MaxFrameBytes-1)
	if err != nil {
		return nil, fmt.Errorf("parse argument file JSON: %w", err)
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
