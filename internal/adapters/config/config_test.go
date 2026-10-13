package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
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
	path := filepath.Join(root, "hub.json")
	inputPath, outputPath := filepath.Join(root, "manual"), filepath.Join(root, "model")
	inputQ, _ := json.Marshal(inputPath)
	outputQ, _ := json.Marshal(outputPath)
	input := `{"name":"manual","role":"input","executable":` + string(inputQ) + `,"args":["$VALUE","a b","$(no shell)"]}`
	output := `{"name":"model","role":"output","executable":` + string(outputQ) + `,"args":[]}`
	good := `{"plugins":[` + input + `,` + output + `]}`
	for _, tc := range []struct {
		name, input string
		invalid     bool
	}{
		{"valid", good, false},
		{"malformed", `{"plugins":`, true},
		{"array", `[]`, true},
		{"BOM", "\ufeff{}", true},
		{"UTF8", "{\"x\":\"\xff\"}", true},
		{"surrogate", `{"plugins":[{"name":"m","role":"input","executable":"/m","args":["\ud800"]}]}`, true},
		{"duplicate keys", `{"plugins":[],"plugins":[]}`, true},
		{"unknown", `{"plugins":[],"extra":1}`, true},
		{"missing", `{}`, true},
		{"empty", `{"plugins":[]}`, true},
		{"input only", `{"plugins":[` + input + `]}`, true},
		{"output only", `{"plugins":[` + output + `]}`, true},
		{"plugin nonarray", `{"plugins":{}}`, true},
		{"duplicate names", `{"plugins":[` + input + `,{"name":"manual","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
		{"unknown plugin field", `{"plugins":[{"name":"m","role":"input","executable":` + string(inputQ) + `,"args":[],"extra":1},{"name":"o","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
		{"unknown role", `{"plugins":[{"name":"m","role":"sideways","executable":` + string(inputQ) + `,"args":[]},{"name":"o","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
		{"relative plugin", `{"plugins":[{"name":"m","role":"input","executable":"manual","args":[]},{"name":"o","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
		{"null args", `{"plugins":[{"name":"m","role":"input","executable":` + string(inputQ) + `,"args":null},{"name":"o","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
		{"number arg", `{"plugins":[{"name":"m","role":"input","executable":` + string(inputQ) + `,"args":[1]},{"name":"o","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
		{"empty name", `{"plugins":[{"name":"","role":"input","executable":` + string(inputQ) + `,"args":[]},{"name":"o","role":"output","executable":` + string(outputQ) + `,"args":[]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := NewMockfileSystem(gomock.NewController(t))
			files.EXPECT().Stat(path).Return(fileInfo{}, nil)
			files.EXPECT().ReadFile(path).Return([]byte(tc.input), nil)
			if !tc.invalid {
				files.EXPECT().Stat(inputPath).Return(fileInfo{}, nil)
				files.EXPECT().Stat(outputPath).Return(fileInfo{}, nil)
			}
			got, err := load(path, files)
			if (err != nil) != tc.invalid {
				t.Fatalf("load: %v", err)
			}
			if !tc.invalid && (len(got.Plugins) != 2 || got.Plugins[0].Role != domain.InputRole || got.Plugins[1].Role != domain.OutputRole || got.Plugins[0].Args[0] != "$VALUE" || got.Plugins[0].Args[2] != "$(no shell)") {
				t.Fatalf("modified configuration: %#v", got)
			}
		})
	}
	for _, stage := range []string{"relative config", "missing config", "directory config", "read config", "missing input", "directory input", "missing output", "directory output"} {
		t.Run(stage, func(t *testing.T) {
			files := NewMockfileSystem(gomock.NewController(t))
			failure := errors.New("fixture failure")
			inputPathArg := path
			switch stage {
			case "relative config":
				inputPathArg = "relative.json"
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
				if stage == "missing input" {
					files.EXPECT().Stat(inputPath).Return(nil, failure)
					break
				}
				if stage == "directory input" {
					files.EXPECT().Stat(inputPath).Return(fileInfo{os.ModeDir}, nil)
					break
				}
				files.EXPECT().Stat(inputPath).Return(fileInfo{}, nil)
				if stage == "missing output" {
					files.EXPECT().Stat(outputPath).Return(nil, failure)
				} else {
					files.EXPECT().Stat(outputPath).Return(fileInfo{os.ModeDir}, nil)
				}
			}
			if _, err := load(inputPathArg, files); err == nil {
				t.Fatal("ignored invalid path or filesystem fault")
			}
		})
	}
}
