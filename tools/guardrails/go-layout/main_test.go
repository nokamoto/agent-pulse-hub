package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRepository(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"empty repository", nil, ""},
		{"root module", map[string]string{"go.mod": "module example.com/app\n"}, ""},
		{"root tools", map[string]string{"tools.go": "//go:build tools\n\npackage tools\n"}, ""},
		{"build entry points", map[string]string{"build/mage.go": "package main", "build/Magefile.go": "//go:build mage\n\npackage main"}, ""},
		{"command entry point", map[string]string{"cmd/server/main.go": "package main"}, ""},
		{"internal responsibility", map[string]string{"internal/config/config.go": "package config"}, ""},
		{"nested internal implementation", map[string]string{"internal/config/parser/parser.go": "package parser"}, ""},
		{"guardrail entry point", map[string]string{"tools/guardrails/example/main.go": "package main"}, ""},
		{"nested guardrail implementation", map[string]string{"tools/guardrails/example/parser/parser.go": "package parser"}, ""},
		{"non-Go files", map[string]string{"docs/example.md": "text", "config.json": "{}"}, ""},
		{"root implementation", map[string]string{"main.go": "package main"}, "main.go: Go source must"},
		{"root test", map[string]string{"tools_test.go": "package tools"}, "tools_test.go: Go source must"},
		{"nested build implementation", map[string]string{"build/helper/helper.go": "package helper"}, "build/helper/helper.go: Go source must"},
		{"command root", map[string]string{"cmd/main.go": "package main"}, "cmd/main.go: Go source must"},
		{"nested command implementation", map[string]string{"cmd/server/config/config.go": "package config"}, "cmd/server/config/config.go: Go source must"},
		{"internal root", map[string]string{"internal/config.go": "package config"}, "internal/config.go: Go source must"},
		{"tools root", map[string]string{"tools/helper.go": "package helper"}, "tools/helper.go: Go source must"},
		{"guardrails root", map[string]string{"tools/guardrails/helper.go": "package helper"}, "tools/guardrails/helper.go: Go source must"},
		{"other tool", map[string]string{"tools/helper/main.go": "package main"}, "tools/helper/main.go: Go source must"},
		{"other directory", map[string]string{"pkg/config/config.go": "package config"}, "pkg/config/config.go: Go source must"},
		{"vendor source", map[string]string{"vendor/example.com/lib/lib.go": "package lib"}, "vendor/example.com/lib/lib.go: Go source must"},
		{"Windows source is inspected", map[string]string{"pkg/main_windows.go": "//go:build windows\n\npackage main"}, "pkg/main_windows.go: Go source must"},
		{"unselected build tag is inspected", map[string]string{"main.go": "//go:build neverselected\n\npackage main"}, "main.go: Go source must"},
		{"ignored filename is inspected", map[string]string{"_helper.go": "package helper"}, "_helper.go: Go source must"},
		{"test fixtures", map[string]string{"tools/guardrails/example/testdata/invalid/main.go": "invalid Go", "tools/guardrails/example/testdata/module/go.mod": "invalid module"}, ""},
		{"root test fixtures", map[string]string{"testdata/main.go": "invalid Go", "testdata/go.mod": "invalid module"}, ""},
		{"git directory", map[string]string{".git/example.go": "invalid Go", ".git/go.mod": "invalid module"}, ""},
		{"worktree git file", map[string]string{".git": "gitdir: elsewhere"}, ""},
		{"nested module in allowed source directory", map[string]string{"internal/config/go.mod": "module example.com/config"}, "internal/config/go.mod: nested go.mod"},
		{"nested module in unrelated directory", map[string]string{"examples/go.mod": "module example.com/example"}, "examples/go.mod: nested go.mod"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for path, content := range tt.files {
				writeFile(t, root, path, content)
			}
			errors := validateRepository(root)
			if tt.want == "" {
				if len(errors) != 0 {
					t.Fatalf("valid layout rejected: %v", errors)
				}
				return
			}
			if len(errors) != 1 || !strings.Contains(errors[0].Error(), tt.want) {
				t.Fatalf("errors = %v; want one error containing %q", errors, tt.want)
			}
		})
	}
}

func TestEmptyDirectoriesAreOptional(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"cmd", "internal", "tools/guardrails", "build", "unrelated"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if errors := validateRepository(root); len(errors) != 0 {
		t.Fatalf("empty directories rejected: %v", errors)
	}
}

func TestValidateRepositoryReportsAllViolations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "internal/go.mod", "module nested")
	writeFile(t, root, "pkg/helper.go", "package helper")
	errors := validateRepository(root)
	want := []string{"internal/go.mod:", "main.go:", "pkg/helper.go:"}
	if len(errors) != len(want) {
		t.Fatalf("errors = %v; want %d violations", errors, len(want))
	}
	for i, prefix := range want {
		if !strings.HasPrefix(errors[i].Error(), prefix) {
			t.Errorf("error %d = %q; want prefix %q", i, errors[i], prefix)
		}
	}
}

func TestValidateRepositoryRootErrors(t *testing.T) {
	root := t.TempDir()
	file := writeFile(t, root, "file", "text")
	for _, path := range []string{filepath.Join(root, "missing"), file} {
		errors := validateRepository(path)
		if len(errors) != 1 || !strings.Contains(errors[0].Error(), path) {
			t.Errorf("errors = %v; want filesystem error containing %q", errors, path)
		}
	}
}

func TestValidateRepositorySymlinks(t *testing.T) {
	for _, name := range []string{"file.go", "directory", "testdata", "root"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			target := t.TempDir()
			writeFile(t, target, "main.go", "package main")
			link := filepath.Join(root, name)
			if name == "file.go" {
				target = filepath.Join(target, "main.go")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if name == "root" {
				root = link
			}
			errors := validateRepository(root)
			if len(errors) != 1 || !strings.Contains(errors[0].Error(), "symlinks are not allowed") || !strings.Contains(errors[0].Error(), name) {
				t.Fatalf("errors = %v; want symlink error for %s", errors, name)
			}
		})
	}
}

func writeFile(t *testing.T, root, path, content string) string {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return fullPath
}
