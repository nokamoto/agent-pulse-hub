package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixtures are materialized independently for each role and missing artifact.
func TestRequiredArtifacts(t *testing.T) {
	for _, role := range []string{"triggers", "actions"} {
		for _, tt := range []struct {
			name, remove, want string
		}{
			{"complete", "", ""},
			{"missing implementation", "main.go", "missing implementation"},
			{"missing README", "readme.md", "missing lowercase readme.md"},
			{"missing examples", "examples/config.json", "missing configuration examples"},
		} {
			t.Run(role+"/"+tt.name, func(t *testing.T) {
				root := t.TempDir()
				plugin := "plugins/" + role + "/example"
				completeFixture(t, root, plugin)
				if tt.remove != "" {
					if err := os.Remove(filepath.Join(root, plugin, tt.remove)); err != nil {
						t.Fatal(err)
					}
				}
				violations := validateRepository(root)
				if tt.want == "" {
					if len(violations) != 0 {
						t.Fatalf("complete plugin rejected: %v", violations)
					}
				} else if len(violations) != 1 || !strings.Contains(violations[0].Error(), plugin) || !strings.Contains(violations[0].Error(), tt.want) {
					t.Fatalf("violations = %v; want plugin path and %q", violations, tt.want)
				}
			})
		}
	}
}

func TestInvalidArtifacts(t *testing.T) {
	for _, tt := range []struct {
		name, artifact, replacement, content, want string
	}{
		{"test only", "main.go", "main_test.go", "package main", "implementation"},
		{"fixture only", "main.go", "testdata/main.go", "package main", "implementation"},
		{"ignored source", "main.go", "_main.go", "package main", "implementation"},
		{"nested implementation only", "main.go", "internal/adapters/plugin.go", "package adapters", "implementation"},
		{"empty source", "main.go", "main.go", "", "implementation"},
		{"uppercase README", "readme.md", "README.md", "instructions", "readme.md"},
		{"empty README", "readme.md", "readme.md", "", "readme.md"},
		{"empty example", "examples/config.json", "examples/config.json", "", "examples"},
		{"example instructions only", "examples/config.json", "examples/readme.md", "instructions", "examples"},
		{"nested example only", "examples/config.json", "examples/nested/config.json", "{}", "examples"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			plugin := "plugins/triggers/example"
			completeFixture(t, root, plugin)
			if err := os.Remove(filepath.Join(root, plugin, tt.artifact)); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, root, plugin+"/"+tt.replacement, tt.content)
			violations := validateRepository(root)
			if len(violations) != 1 || !strings.Contains(violations[0].Error(), tt.want) {
				t.Fatalf("invalid artifact accepted: %v", violations)
			}
		})
	}
}

func TestDiscoveryAndAllMissing(t *testing.T) {
	root := t.TempDir()
	if violations := validateRepository(root); len(violations) != 0 {
		t.Fatal(violations)
	}
	completeFixture(t, root, "plugins/triggers/first")
	completeFixture(t, root, "plugins/actions/second")
	if err := os.MkdirAll(filepath.Join(root, "plugins/actions/empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	violations := validateRepository(root)
	if len(violations) != 3 {
		t.Fatalf("want all three missing artifacts for discovered empty plugin, got %v", violations)
	}
	for _, err := range violations {
		if !strings.Contains(err.Error(), "plugins/actions/empty") {
			t.Fatal(err)
		}
	}
}

func TestSymlinksAndDirectories(t *testing.T) {
	for _, path := range []string{"plugins", "plugins/triggers", "plugins/triggers/example", "plugins/triggers/example/main.go", "plugins/triggers/example/readme.md", "plugins/triggers/example/examples", "plugins/triggers/example/examples/config.json"} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			completeFixture(t, root, "plugins/triggers/example")
			full := filepath.Join(root, path)
			if err := os.RemoveAll(full); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), full); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if violations := validateRepository(root); len(violations) == 0 {
				t.Fatal("symlink accepted")
			}
		})
	}
	root := t.TempDir()
	writeFixture(t, root, "plugins/actions/not-a-directory", "text")
	if violations := validateRepository(root); len(violations) != 1 {
		t.Fatalf("file masquerading as plugin accepted: %v", violations)
	}
}

func completeFixture(t *testing.T, root, plugin string) {
	t.Helper()
	writeFixture(t, root, plugin+"/main.go", "package main\nfunc main() {}\n")
	writeFixture(t, root, plugin+"/readme.md", "# Fixture plugin\n")
	writeFixture(t, root, plugin+"/examples/config.json", "{}\n")
}

func writeFixture(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
