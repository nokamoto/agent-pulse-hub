package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	violations := validateRepository(".")
	if len(violations) != 0 {
		fmt.Fprintln(os.Stderr, "Bundled plugin artifact validation failed:")
		for _, err := range violations {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Println("All bundled plugins contain the required artifacts")
}

func validateRepository(root string) []error {
	plugins := filepath.Join(root, "plugins")
	info, err := os.Lstat(plugins)
	if os.IsNotExist(err) {
		return nil // No bundled plugins have been introduced yet.
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return []error{fmt.Errorf("plugins: must be a readable directory, not a symlink (%v)", err)}
	}
	var violations []error
	for _, role := range []string{"triggers", "actions"} {
		rolePath := filepath.Join(plugins, role)
		info, err := os.Lstat(rolePath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			violations = append(violations, fmt.Errorf("plugins/%s: must be a readable directory, not a symlink (%v)", role, err))
			continue
		}
		entries, err := os.ReadDir(rolePath)
		if err != nil {
			violations = append(violations, fmt.Errorf("plugins/%s: cannot discover plugins: %w", role, err))
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(rolePath, entry.Name())
			rel := "plugins/" + role + "/" + entry.Name()
			if !entry.IsDir() {
				violations = append(violations, fmt.Errorf("%s: plugin entries must be directories, not files or symlinks", rel))
				continue
			}
			if err := hasArtifact(path, func(name string) bool {
				return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_")
			}); err != nil {
				violations = append(violations, fmt.Errorf("%s: missing implementation (nonempty non-test *.go directly in the plugin): %w", rel, err))
			}
			if err := hasArtifact(path, func(name string) bool { return name == "readme.md" }); err != nil {
				violations = append(violations, fmt.Errorf("%s: missing lowercase readme.md: %w", rel, err))
			}
			if err := hasArtifact(filepath.Join(path, "examples"), func(name string) bool { return strings.HasSuffix(name, ".json") }); err != nil {
				violations = append(violations, fmt.Errorf("%s: missing configuration examples (nonempty examples/*.json): %w", rel, err))
			}
		}
	}
	return violations
}

func hasArtifact(directory string, matches func(string) bool) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a directory, not a symlink", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if matches(entry.Name()) && nonemptyFile(filepath.Join(directory, entry.Name())) == nil {
			return nil
		}
	}
	return fmt.Errorf("no matching nonempty regular file")
}

func nonemptyFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("must be a nonempty regular file; symlinks are not allowed")
	}
	return nil
}
