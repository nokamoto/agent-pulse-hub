package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/jsonstrict"
)

const MaxConfigBytes = 1024 * 1024

type Plugin struct {
	Name       string     `json:"name"`
	Executable string     `json:"executable"`
	Args       StringArgs `json:"args"`
}

type StringArgs []string

func (arguments *StringArgs) UnmarshalJSON(data []byte) error {
	var rawArguments []json.RawMessage
	if err := json.Unmarshal(data, &rawArguments); err != nil {
		return err
	}
	if rawArguments == nil {
		*arguments = nil
		return nil
	}
	decoded := make(StringArgs, len(rawArguments))
	for index, rawArgument := range rawArguments {
		rawArgument = bytes.TrimSpace(rawArgument)
		if len(rawArgument) == 0 || rawArgument[0] != '"' {
			return fmt.Errorf("argument %d must be a string", index)
		}
		if err := json.Unmarshal(rawArgument, &decoded[index]); err != nil {
			return fmt.Errorf("decode argument %d: %w", index, err)
		}
	}
	*arguments = decoded
	return nil
}

type Config struct {
	CodexExecutable string   `json:"codex_executable"`
	Plugins         []Plugin `json:"plugins"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("configuration exceeds %d bytes", MaxConfigBytes)
	}
	return Decode(data)
}

func Decode(data []byte) (Config, error) {
	var value Config
	if err := jsonstrict.Decode(data, &value); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (value Config) Validate() error {
	if strings.TrimSpace(value.CodexExecutable) == "" {
		return errors.New("codex_executable is required")
	}
	if !filepath.IsAbs(value.CodexExecutable) {
		return errors.New("codex_executable must be an absolute path")
	}
	if len(value.Plugins) == 0 {
		return errors.New("plugins must contain at least one entry")
	}
	seen := make(map[string]struct{}, len(value.Plugins))
	for index, plugin := range value.Plugins {
		if strings.TrimSpace(plugin.Name) == "" {
			return fmt.Errorf("plugins[%d].name is required", index)
		}
		if _, exists := seen[plugin.Name]; exists {
			return fmt.Errorf("duplicate plugin name %q", plugin.Name)
		}
		seen[plugin.Name] = struct{}{}
		if strings.TrimSpace(plugin.Executable) == "" || !filepath.IsAbs(plugin.Executable) {
			return fmt.Errorf("plugins[%d].executable must be an absolute path", index)
		}
		if plugin.Args == nil {
			return fmt.Errorf("plugins[%d].args must be an array", index)
		}
		for argIndex, arg := range plugin.Args {
			if !utf8.ValidString(arg) {
				return fmt.Errorf("plugins[%d].args[%d] is not valid UTF-8", index, argIndex)
			}
		}
	}
	return nil
}
