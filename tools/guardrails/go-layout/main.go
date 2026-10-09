package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	errors := validateRepository(".")
	if len(errors) > 0 {
		fmt.Fprintln(os.Stderr, "Go layout validation failed:")
		for _, err := range errors {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Println("All Go source files follow the repository layout")
}

// validateRepository inspects the filesystem independently of Go build constraints.
func validateRepository(root string) []error {
	info, err := os.Lstat(root)
	if err != nil {
		return []error{fmt.Errorf("%s: cannot inspect repository root: %w", root, err)}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []error{fmt.Errorf("%s: symlinks are not allowed", root)}
	}
	if !info.IsDir() {
		return []error{fmt.Errorf("%s: repository root must be a directory", root)}
	}

	var errors []error
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			errors = append(errors, fmt.Errorf("%s: cannot determine repository-relative path: %w", path, err))
			return err
		}
		rel = filepath.ToSlash(rel)
		if walkErr != nil {
			errors = append(errors, fmt.Errorf("%s: cannot inspect filesystem entry: %w", rel, walkErr))
			return nil
		}
		if entry.Name() == ".git" || (entry.IsDir() && entry.Name() == "testdata") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			errors = append(errors, fmt.Errorf("%s: symlinks are not allowed", rel))
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "go.mod" && rel != "go.mod" {
			errors = append(errors, fmt.Errorf("%s: nested go.mod is not allowed; use the repository root module", rel))
		}
		if filepath.Ext(rel) != ".go" && entry.Name() != "go.mod" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			errors = append(errors, fmt.Errorf("%s: cannot inspect file: %w", rel, err))
			return nil
		}
		if !info.Mode().IsRegular() {
			errors = append(errors, fmt.Errorf("%s: Go source files and go.mod must be regular files", rel))
			return nil
		}
		if filepath.Ext(rel) == ".go" && !allowedGoPath(rel) {
			errors = append(errors, fmt.Errorf("%s: Go source must be tools.go at the root, build/*.go, cmd/<command>/*.go, internal/<responsibility>/**/*.go, or tools/guardrails/<validator>/**/*.go", rel))
		}
		return nil
	})
	if err != nil {
		errors = append(errors, fmt.Errorf("%s: cannot walk repository: %w", root, err))
	}
	return errors
}

// allowedGoPath receives a repository-relative path with slash separators.
func allowedGoPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		return path == "tools.go"
	}
	switch parts[0] {
	case "build":
		return len(parts) == 2
	case "cmd":
		return len(parts) == 3
	case "internal":
		return len(parts) >= 3
	case "tools":
		return len(parts) >= 4 && parts[1] == "guardrails"
	default:
		return false
	}
}
