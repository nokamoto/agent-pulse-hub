package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var allowedDocsDirectories = []string{"aidd", "design", "requirements"}

func main() {
	errors := validateRepository(".")
	if len(errors) > 0 {
		fmt.Fprintln(os.Stderr, "Documentation layout validation failed:")
		for _, err := range errors {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}
		os.Exit(1)
	}
	fmt.Println("All top-level documentation directories are allowed")
}

func validateRepository(root string) []error {
	docsPath := filepath.Join(root, "docs")
	info, err := os.Lstat(docsPath)
	if err != nil {
		return []error{fmt.Errorf("%s: cannot inspect documentation directory: %w", displayPath(docsPath), err)}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []error{fmt.Errorf("%s: documentation directory must not be a symlink", displayPath(docsPath))}
	}
	if !info.IsDir() {
		return []error{fmt.Errorf("%s: documentation path must be a directory", displayPath(docsPath))}
	}

	entries, err := os.ReadDir(docsPath)
	if err != nil {
		return []error{fmt.Errorf("%s: cannot read documentation directory: %w", displayPath(docsPath), err)}
	}

	var errors []error
	for _, entry := range entries {
		path := filepath.Join(docsPath, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			errors = append(errors, fmt.Errorf("%s: cannot inspect documentation entry: %w", displayPath(path), err))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			errors = append(errors, fmt.Errorf("%s: symlinks are not allowed directly under docs", displayPath(path)))
			continue
		}
		if !info.IsDir() {
			if isAllowedDocsDirectory(entry.Name()) {
				errors = append(errors, fmt.Errorf("%s: must be a directory", displayPath(path)))
			}
			continue
		}
		if !isAllowedDocsDirectory(entry.Name()) {
			errors = append(errors, fmt.Errorf("%s: directory is not allowed directly under docs; allowed directories: %s. Update the repository rule and this guardrail before adding a documentation category", displayPath(path), strings.Join(allowedDocsDirectories, ", ")))
		}
	}
	return errors
}

func isAllowedDocsDirectory(name string) bool {
	for _, allowed := range allowedDocsDirectories {
		if name == allowed {
			return true
		}
	}
	return false
}

func displayPath(path string) string {
	return filepath.ToSlash(path)
}
