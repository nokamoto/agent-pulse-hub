package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeValidatesWholeConfiguration(t *testing.T) {
	base := Config{
		CodexExecutable: filepath.Join(t.TempDir(), "codex.exe"),
		Plugins: []Plugin{{
			Name:       "manual",
			Executable: filepath.Join(t.TempDir(), "manual.exe"),
			Args:       StringArgs{},
		}},
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(encoded); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestDecodeRejectsMalformedConfiguration(t *testing.T) {
	absolute := filepath.Join(t.TempDir(), "program.exe")
	base := `{"codex_executable":"` + strings.ReplaceAll(absolute, `\`, `\\`) + `","plugins":[{"name":"manual","executable":"` + strings.ReplaceAll(absolute, `\`, `\\`) + `","args":[]}]}`
	tests := []struct {
		name string
		data string
	}{
		{name: "duplicate top-level key", data: strings.Replace(base, `,"plugins":`, `,"codex_executable":"`+absolute+`","plugins":`, 1)},
		{name: "unknown field", data: strings.Replace(base, `,"args":[]`, `,"args":[],"extra":true`, 1)},
		{name: "duplicate plugin name", data: strings.Replace(base, `]}`, `,{"name":"manual","executable":"`+absolute+`","args":[]}]}`, 1)},
		{name: "missing args array", data: strings.Replace(base, `,"args":[]`, ``, 1)},
		{name: "null argument", data: strings.Replace(base, `,"args":[]`, `,"args":[null]`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode([]byte(test.data)); err == nil {
				t.Fatalf("Decode succeeded for %s", test.data)
			}
		})
	}
}

func TestValidateRequiresAbsolutePaths(t *testing.T) {
	value := Config{CodexExecutable: "codex.exe", Plugins: []Plugin{{Name: "manual", Executable: "manual.exe", Args: StringArgs{}}}}
	if err := value.Validate(); err == nil {
		t.Fatal("relative executable paths were accepted")
	}
}
