package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func makeDocsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateRepositoryAllowsOnlyKnownTopLevelDirectories(t *testing.T) {
	root := makeDocsRoot(t)
	for _, name := range allowedDocsDirectories {
		if err := os.Mkdir(filepath.Join(root, "docs", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "index.md"), []byte("Documentation index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs", "design", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	if errors := validateRepository(root); len(errors) != 0 {
		t.Fatalf("valid documentation layout rejected: %v", errors)
	}
}

func TestValidateRepositoryRejectsUnknownTopLevelDirectory(t *testing.T) {
	root := makeDocsRoot(t)
	unknown := filepath.Join(root, "docs", "protocol")
	if err := os.Mkdir(unknown, 0o755); err != nil {
		t.Fatal(err)
	}

	errors := validateRepository(root)
	if len(errors) != 1 || !strings.Contains(errors[0].Error(), "docs/protocol") || !strings.Contains(errors[0].Error(), "allowed directories: aidd, design, requirements") {
		t.Fatalf("errors = %v; want unknown directory and allowed list", errors)
	}
}

func TestValidateRepositoryUsesExactDirectoryNames(t *testing.T) {
	root := makeDocsRoot(t)
	if err := os.Mkdir(filepath.Join(root, "docs", "Protocol"), 0o755); err != nil {
		t.Fatal(err)
	}

	errors := validateRepository(root)
	if len(errors) != 1 || !strings.Contains(errors[0].Error(), "docs/Protocol") {
		t.Fatalf("errors = %v; want case-sensitive directory rejection", errors)
	}
}

func TestValidateRepositoryRejectsAllowedNameThatIsNotDirectory(t *testing.T) {
	root := makeDocsRoot(t)
	if err := os.WriteFile(filepath.Join(root, "docs", "design"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	errors := validateRepository(root)
	if len(errors) != 1 || !strings.Contains(errors[0].Error(), "docs/design: must be a directory") {
		t.Fatalf("errors = %v; want allowed path type error", errors)
	}
}

func TestValidateRepositoryRejectsSymlinks(t *testing.T) {
	root := makeDocsRoot(t)
	target := filepath.Join(root, "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "docs", "protocol")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	errors := validateRepository(root)
	if len(errors) != 1 || !strings.Contains(errors[0].Error(), "docs/protocol: symlinks are not allowed") {
		t.Fatalf("errors = %v; want symlink rejection", errors)
	}
}

func TestValidateRepositoryRejectsMissingOrInvalidDocsRoot(t *testing.T) {
	root := t.TempDir()
	if errors := validateRepository(root); len(errors) != 1 || !strings.Contains(errors[0].Error(), "cannot inspect documentation directory") {
		t.Fatalf("errors = %v; want missing docs root error", errors)
	}

	fileRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileRoot, "docs"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if errors := validateRepository(fileRoot); len(errors) != 1 || !strings.Contains(errors[0].Error(), "documentation path must be a directory") {
		t.Fatalf("errors = %v; want invalid docs root error", errors)
	}
}

func TestValidateRepositoryRejectsDocsRootSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "docs")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	if errors := validateRepository(root); len(errors) != 1 || !strings.Contains(errors[0].Error(), "documentation directory must not be a symlink") {
		t.Fatalf("errors = %v; want docs-root symlink rejection", errors)
	}
}

func TestUnreadableDocsDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode permissions are not enforced on Windows")
	}
	root := makeDocsRoot(t)
	docs := filepath.Join(root, "docs")
	if err := os.Chmod(docs, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(docs, 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.ReadDir(docs); err == nil {
		t.Skip("current user can read directories without permission bits")
	}

	errors := validateRepository(root)
	if len(errors) != 1 || !strings.Contains(errors[0].Error(), "cannot read documentation directory") {
		t.Fatalf("errors = %v; want docs read error", errors)
	}
}
