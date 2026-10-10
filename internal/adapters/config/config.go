package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type Plugin struct {
	Name       string
	Executable string
	Args       []string
}

type Config struct {
	CodexExecutable string
	Plugins         []Plugin
}

func Load(path string) (Config, error) {
	if !isAbsoluteFile(path) {
		return Config{}, errors.New("configuration path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("open daemon configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("daemon configuration must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read daemon configuration: %w", err)
	}
	value, err := jsonvalue.Parse(data, 0)
	if err != nil {
		return Config{}, fmt.Errorf("parse daemon configuration: %w", err)
	}
	if err := protocol.ValidateFields(value, []string{"codex_executable", "plugins"}, "codex_executable", "plugins"); err != nil {
		return Config{}, errors.New("daemon configuration fields are invalid")
	}
	codexPath, ok := protocol.StringField(value, "codex_executable")
	if !ok || !isAbsoluteFile(codexPath) {
		return Config{}, errors.New("codex_executable must be an absolute path")
	}
	pluginsValue, ok := value.Get("plugins")
	if !ok || pluginsValue.Kind() != jsonvalue.Array {
		return Config{}, errors.New("plugins must be a JSON array")
	}
	plugins := make([]Plugin, 0, len(pluginsValue.Items()))
	seen := make(map[string]struct{})
	for index, pluginValue := range pluginsValue.Items() {
		plugin, err := parsePlugin(pluginValue)
		if err != nil {
			return Config{}, fmt.Errorf("plugin entry %d is invalid: %w", index, err)
		}
		if _, exists := seen[plugin.Name]; exists {
			return Config{}, fmt.Errorf("plugin entry %d duplicates a configured name", index)
		}
		seen[plugin.Name] = struct{}{}
		plugins = append(plugins, plugin)
	}
	if err := validateProgramPath(codexPath, "codex_executable"); err != nil {
		return Config{}, err
	}
	for index, plugin := range plugins {
		if err := validateProgramPath(plugin.Executable, fmt.Sprintf("plugin entry %d executable", index)); err != nil {
			return Config{}, err
		}
	}
	return Config{CodexExecutable: codexPath, Plugins: plugins}, nil
}

func parsePlugin(value *jsonvalue.Value) (Plugin, error) {
	if err := protocol.ValidateFields(value, []string{"name", "executable", "args"}, "name", "executable", "args"); err != nil {
		return Plugin{}, errors.New("fields are invalid")
	}
	name, nameOK := protocol.StringField(value, "name")
	executable, executableOK := protocol.StringField(value, "executable")
	if !nameOK || name == "" || !executableOK || !isAbsoluteFile(executable) {
		return Plugin{}, errors.New("name or absolute executable path is invalid")
	}
	argsValue, ok := value.Get("args")
	if !ok || argsValue.Kind() != jsonvalue.Array {
		return Plugin{}, errors.New("args must be a JSON array of strings")
	}
	args := make([]string, 0, len(argsValue.Items()))
	for _, arg := range argsValue.Items() {
		text, ok := jsonvalue.ParseString(arg)
		if !ok {
			return Plugin{}, errors.New("args must contain only strings")
		}
		args = append(args, text)
	}
	return Plugin{Name: name, Executable: executable, Args: args}, nil
}

func isAbsoluteFile(path string) bool {
	return filepath.IsAbs(path)
}

func validateProgramPath(path, field string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is unavailable: %w", field, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must name a regular file", field)
	}
	return nil
}
