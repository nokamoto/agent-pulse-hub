package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type Plugin struct {
	Name       string
	Role       domain.PluginRole
	Executable string
	Args       []string
}

type Config struct {
	Plugins []Plugin
}

//go:generate go run go.uber.org/mock/mockgen -source=config.go -destination=config_mock_test.go -package=config
type fileSystem interface {
	Stat(string) (os.FileInfo, error)
	ReadFile(string) ([]byte, error)
}

type localFiles struct{}

func (localFiles) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
func (localFiles) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }

func Load(path string) (Config, error) {
	return load(path, localFiles{})
}

func load(path string, files fileSystem) (Config, error) {
	if !filepath.IsAbs(path) {
		return Config{}, errors.New("configuration path must be absolute")
	}
	info, err := files.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("open daemon configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("daemon configuration must be a regular file")
	}
	data, err := files.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read daemon configuration: %w", err)
	}
	value, err := jsonvalue.Parse(data, 0)
	if err != nil {
		return Config{}, fmt.Errorf("parse daemon configuration: %w", err)
	}
	if err := protocol.ValidateFields(value, []string{"plugins"}, "plugins"); err != nil {
		return Config{}, errors.New("daemon configuration fields are invalid")
	}
	pluginsValue, ok := value.Get("plugins")
	if !ok || pluginsValue.Kind() != jsonvalue.Array || len(pluginsValue.Items()) == 0 {
		return Config{}, errors.New("plugins must be a nonempty JSON array")
	}
	plugins := make([]Plugin, 0, len(pluginsValue.Items()))
	seenNames := make(map[string]struct{}, len(pluginsValue.Items()))
	roles := map[domain.PluginRole]bool{}
	for index, pluginValue := range pluginsValue.Items() {
		plugin, err := parsePlugin(pluginValue)
		if err != nil {
			return Config{}, fmt.Errorf("plugin entry %d is invalid: %w", index, err)
		}
		if _, exists := seenNames[plugin.Name]; exists {
			return Config{}, fmt.Errorf("plugin entry %d duplicates a configured name", index)
		}
		seenNames[plugin.Name] = struct{}{}
		roles[plugin.Role] = true
		plugins = append(plugins, plugin)
	}
	if !roles[domain.InputRole] || !roles[domain.OutputRole] {
		return Config{}, errors.New("configuration requires at least one input and one output plugin")
	}
	for index, plugin := range plugins {
		if err := validateProgramPath(files, plugin.Executable, fmt.Sprintf("plugin entry %d executable", index)); err != nil {
			return Config{}, err
		}
	}
	return Config{Plugins: plugins}, nil
}

func parsePlugin(value *jsonvalue.Value) (Plugin, error) {
	if err := protocol.ValidateFields(value,
		[]string{"name", "role", "executable", "args"},
		"name", "role", "executable", "args"); err != nil {
		return Plugin{}, errors.New("fields are invalid")
	}
	name, nameOK := protocol.StringField(value, "name")
	roleText, roleOK := protocol.StringField(value, "role")
	executable, executableOK := protocol.StringField(value, "executable")
	role := domain.PluginRole(roleText)
	if !nameOK || name == "" || !roleOK || (role != domain.InputRole && role != domain.OutputRole) || !executableOK || !filepath.IsAbs(executable) {
		return Plugin{}, errors.New("name, role, or absolute executable path is invalid")
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
	return Plugin{Name: name, Role: role, Executable: executable, Args: args}, nil
}

func validateProgramPath(files fileSystem, path, field string) error {
	info, err := files.Stat(path)
	if err != nil {
		return fmt.Errorf("%s is unavailable: %w", field, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must name a regular file", field)
	}
	return nil
}
