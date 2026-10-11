package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

type fileInfo struct{ mode os.FileMode }

func (fileInfo) Name() string        { return "fixture" }
func (fileInfo) Size() int64         { return 10 }
func (f fileInfo) Mode() os.FileMode { return f.mode }
func (fileInfo) ModTime() time.Time  { return time.Time{} }
func (f fileInfo) IsDir() bool       { return f.mode.IsDir() }
func (fileInfo) Sys() any            { return nil }

func TestLoadConfigurationDecisions(t *testing.T) {
	root, err := filepath.Abs("fixture")
	if err != nil {
		t.Fatal(err)
	}
	path, codex, plugin := filepath.Join(root, "hub.json"), filepath.Join(root, "codex.exe"), filepath.Join(root, "manual.exe")
	cq, _ := json.Marshal(codex)
	pq, _ := json.Marshal(plugin)
	entry := `{"name":"manual","executable":` + string(pq) + `,"args":["$VALUE","a b","$(no shell)"]}`
	good := `{"codex_executable":` + string(cq) + `,"plugins":[` + entry + `]}`
	for _, tc := range []struct {
		name, input string
		invalid     bool
	}{
		{"valid", good, false},
		{"malformed", `{"plugins":`, true},
		{"array", `[]`, true},
		{"BOM", "\ufeff{}", true},
		{"UTF8", "{\"x\":\"\xff\"}", true},
		{"surrogate", `{"codex_executable":"\ud800","plugins":[]}`, true},
		{"duplicate keys", `{"plugins":[],"plugins":[],"codex_executable":` + string(cq) + `}`, true},
		{"unknown", `{"codex_executable":` + string(cq) + `,"plugins":[],"extra":1}`, true},
		{"missing", `{"plugins":[]}`, true},
		{"relative executable", `{"codex_executable":"codex.exe","plugins":[]}`, true},
		{"plugin nonarray", `{"codex_executable":` + string(cq) + `,"plugins":{}}`, true},
		{"duplicate names", `{"codex_executable":` + string(cq) + `,"plugins":[` + entry + `,` + entry + `]}`, true},
		{"unknown plugin field", `{"codex_executable":` + string(cq) + `,"plugins":[{"name":"m","executable":` + string(pq) + `,"args":[],"extra":1}]}`, true},
		{"relative plugin", `{"codex_executable":` + string(cq) + `,"plugins":[{"name":"m","executable":"manual.exe","args":[]}]}`, true},
		{"null args", `{"codex_executable":` + string(cq) + `,"plugins":[{"name":"m","executable":` + string(pq) + `,"args":null}]}`, true},
		{"number arg", `{"codex_executable":` + string(cq) + `,"plugins":[{"name":"m","executable":` + string(pq) + `,"args":[1]}]}`, true},
		{"empty name", `{"codex_executable":` + string(cq) + `,"plugins":[{"name":"","executable":` + string(pq) + `,"args":[]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := NewMockfileSystem(gomock.NewController(t))
			files.EXPECT().Stat(path).Return(fileInfo{}, nil)
			files.EXPECT().ReadFile(path).Return([]byte(tc.input), nil)
			if !tc.invalid {
				files.EXPECT().Stat(codex).Return(fileInfo{}, nil)
				files.EXPECT().Stat(plugin).Return(fileInfo{}, nil)
			}
			got, err := load(path, files)
			if (err != nil) != tc.invalid {
				t.Fatalf("load: %v", err)
			}
			if !tc.invalid && (len(got.Plugins) != 1 || got.Plugins[0].Args[0] != "$VALUE" || got.Plugins[0].Args[2] != "$(no shell)") {
				t.Fatalf("modified configuration: %#v", got)
			}
		})
	}
	for _, stage := range []string{"relative config", "missing config", "directory config", "read config", "missing codex", "directory codex", "missing plugin", "directory plugin"} {
		t.Run(stage, func(t *testing.T) {
			files := NewMockfileSystem(gomock.NewController(t))
			failure := errors.New("fixture failure")
			input := path
			switch stage {
			case "relative config":
				input = "relative.json"
			case "missing config":
				files.EXPECT().Stat(path).Return(nil, failure)
			case "directory config":
				files.EXPECT().Stat(path).Return(fileInfo{os.ModeDir}, nil)
			default:
				files.EXPECT().Stat(path).Return(fileInfo{}, nil)
				if stage == "read config" {
					files.EXPECT().ReadFile(path).Return(nil, failure)
					break
				}
				files.EXPECT().ReadFile(path).Return([]byte(good), nil)
				if stage == "missing codex" {
					files.EXPECT().Stat(codex).Return(nil, failure)
					break
				}
				if stage == "directory codex" {
					files.EXPECT().Stat(codex).Return(fileInfo{os.ModeDir}, nil)
					break
				}
				files.EXPECT().Stat(codex).Return(fileInfo{}, nil)
				if stage == "missing plugin" {
					files.EXPECT().Stat(plugin).Return(nil, failure)
				} else {
					files.EXPECT().Stat(plugin).Return(fileInfo{os.ModeDir}, nil)
				}
			}
			if _, err := load(input, files); err == nil {
				t.Fatal("ignored invalid path or filesystem fault")
			}
		})
	}
}
