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
	"unicode/utf8"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/adapters/codexqueue"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/adapters/localprocess"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/adapters/registration"
	"github.com/nokamoto/agent-pulse-hub/plugins/actions/codex-queue/internal/application"
	"google.golang.org/grpc"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "register-current" {
		os.Exit(runRegisterCurrent(os.Args[2:], os.Stdout, os.Stderr, os.LookupEnv))
	}
	if err := runPlugin(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "codex-queue:", safeText(err.Error(), 512))
		os.Exit(1)
	}
}

func runPlugin(arguments []string) error {
	executable, err := parsePluginArguments(arguments)
	if err != nil {
		return err
	}
	if err := requireRegularAbsoluteFile(executable, "Codex executable"); err != nil {
		return err
	}
	runner := localprocess.Runner{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.VerifyCodexCompatibility(ctx, executable, runner); err != nil {
		return fmt.Errorf("codex readiness check failed: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: pluginprocess.HandshakeConfig,
		Plugins: plugin.PluginSet{
			pluginprocess.PluginKey(string(domain.OutputRole)): &pluginprocess.FrameworkPlugin{
				Impl: codexqueue.New(executable, application.NewDeliveryService(runner), logger),
			},
		},
		GRPCServer: func(options []grpc.ServerOption) *grpc.Server {
			options = append(options, pluginprotocol.ServerOptions()...)
			return grpc.NewServer(options...)
		},
		Logger: hclog.NewNullLogger(),
	})
	return nil
}

func parsePluginArguments(arguments []string) (string, error) {
	if len(arguments) != 2 || arguments[0] != "--codex-executable" || arguments[1] == "" {
		return "", errors.New("plugin mode requires exactly --codex-executable <absolute-file-path>")
	}
	if !filepath.IsAbs(arguments[1]) {
		return "", errors.New("--codex-executable must be an absolute file path")
	}
	return arguments[1], nil
}

func runRegisterCurrent(arguments []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runRegisterCurrentContext(ctx, arguments, stdout, stderr, lookupEnv)
}

func runRegisterCurrentContext(ctx context.Context, arguments []string, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	config, err := parseRegisterArguments(arguments)
	if err != nil {
		fmt.Fprintln(stderr, "register-current failed:", safeText(err.Error(), 512))
		return 1
	}
	if err := validateRegisterConfig(config); err != nil {
		fmt.Fprintln(stderr, "register-current failed:", safeText(err.Error(), 512))
		return 1
	}
	service := application.NewRegistrationService(localprocess.Runner{}, registration.TempDestinationFiles{})
	result := service.RegisterCurrent(ctx, config, environmentFunc(lookupEnv))
	if result.CleanupError != nil {
		fmt.Fprintln(stderr, "register-current warning: temporary destination cleanup failed")
	}
	switch result.Status {
	case "accepted":
		written, err := stdout.Write(result.Response)
		if err != nil || written != len(result.Response) {
			fmt.Fprintln(stderr, "register-current outcome unknown: the registration may be active but its confirmation could not be written")
			return 1
		}
		return 0
	case "failed":
		if result.Code != "" {
			fmt.Fprintf(stderr, "register-current rejected: code=%q message=%q\n", safeText(result.Code, 256), safeText(result.Message, 1_024))
		} else {
			fmt.Fprintln(stderr, "register-current failed:", safeText(result.Reason, 512))
		}
		return 1
	default:
		fmt.Fprintln(stderr, "register-current outcome unknown:", safeText(result.Reason, 512), "; inspect the target before another attempt")
		return 1
	}
}

func parseRegisterArguments(arguments []string) (application.RegisterConfig, error) {
	allowed := map[string]bool{
		"--client-executable": false,
		"--address":           false,
		"--input-plugin":      false,
		"--watch-args-file":   false,
		"--output-plugin":     false,
	}
	values := make(map[string]string, len(allowed))
	if len(arguments) != len(allowed)*2 {
		return application.RegisterConfig{}, errors.New("all five registration flags require exactly one value")
	}
	for index := 0; index < len(arguments); index += 2 {
		name, value := arguments[index], arguments[index+1]
		seen, exists := allowed[name]
		if !exists {
			return application.RegisterConfig{}, fmt.Errorf("unknown registration flag %q", name)
		}
		if seen {
			return application.RegisterConfig{}, fmt.Errorf("registration flag %q was supplied more than once", name)
		}
		if value == "" {
			return application.RegisterConfig{}, fmt.Errorf("registration flag %q requires a nonempty value", name)
		}
		allowed[name] = true
		values[name] = value
	}
	for name, seen := range allowed {
		if !seen {
			return application.RegisterConfig{}, fmt.Errorf("missing registration flag %q", name)
		}
	}
	return application.RegisterConfig{
		ClientExecutable: values["--client-executable"],
		Address:          values["--address"],
		InputPlugin:      values["--input-plugin"],
		WatchArgsFile:    values["--watch-args-file"],
		OutputPlugin:     values["--output-plugin"],
	}, nil
}

func validateRegisterConfig(config application.RegisterConfig) error {
	if config.Address == "" || config.InputPlugin == "" || config.OutputPlugin == "" {
		return errors.New("address and selected plugin names must be nonempty")
	}
	if err := requireRegularAbsoluteFile(config.ClientExecutable, "hub client executable"); err != nil {
		return err
	}
	if err := requireRegularAbsoluteFile(config.WatchArgsFile, "watch arguments file"); err != nil {
		return err
	}
	file, err := os.Open(config.WatchArgsFile)
	if err != nil {
		return errors.New("watch arguments file is not readable")
	}
	if err := file.Close(); err != nil {
		return errors.New("watch arguments file could not be closed")
	}
	return nil
}

func requireRegularAbsoluteFile(path, label string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute file path", label)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be an existing regular file", label)
	}
	return nil
}

type environmentFunc func(string) (string, bool)

func (f environmentFunc) Lookup(name string) (string, bool) { return f(name) }

func safeText(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, value)
}
