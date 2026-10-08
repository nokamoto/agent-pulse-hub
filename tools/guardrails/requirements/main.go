package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// RequiredFields defines the required YAML fields in order for docs/requirements
var RequiredFields = []string{"type", "title", "description"}

// AllowedFields defines all allowed fields (required + optional, in order)
var AllowedFields = []string{"type", "title", "description", "sources"}

type ValidationError struct {
	File    string
	Message string
}

func main() {
	var allErrors []ValidationError

	// Validate directories
	dirsToValidate := []string{
		"docs/requirements",
		".agents/skills/define-requirements/assets",
	}

	for _, dir := range dirsToValidate {
		errors := validateRequirementsDir(dir)
		allErrors = append(allErrors, errors...)
	}

	// Validate specific files
	filesToValidate := []string{
		".agents/skills/define-requirements/assets/requirement.md",
	}

	for _, file := range filesToValidate {
		errors := validateFile(file)
		allErrors = append(allErrors, errors...)
	}

	if len(allErrors) > 0 {
		fmt.Fprintf(os.Stderr, "Validation failed:\n")
		for _, err := range allErrors {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", err.File, err.Message)
		}
		os.Exit(1)
	}

	fmt.Println("All requirements files are valid")
	os.Exit(0)
}

func validateRequirementsDir(dir string) []ValidationError {
	var errors []ValidationError

	// Get all markdown files in the directory
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		errors = append(errors, ValidationError{
			File:    dir,
			Message: fmt.Sprintf("failed to read directory: %v", err),
		})
		return errors
	}

	// If no files found, that's okay (empty requirements)
	if len(files) == 0 {
		return errors
	}

	for _, file := range files {
		fileErrors := validateFile(file)
		errors = append(errors, fileErrors...)
	}

	return errors
}

func validateFile(filePath string) []ValidationError {
	var errors []ValidationError

	file, err := os.Open(filePath)
	if err != nil {
		errors = append(errors, ValidationError{
			File:    filePath,
			Message: fmt.Sprintf("failed to open file: %v", err),
		})
		return errors
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	content, frontmatter, err := extractFrontmatter(filePath, scanner)
	if err != nil {
		errors = append(errors, ValidationError{
			File:    filePath,
			Message: err.Error(),
		})
		return errors
	}

	// Validate frontmatter
	if content != "" {
		frontmatterErrors := validateFrontmatter(filePath, frontmatter)
		errors = append(errors, frontmatterErrors...)
	}

	return errors
}

func extractFrontmatter(filePath string, scanner *bufio.Scanner) (string, string, error) {
	// Read first line - should be ---
	if !scanner.Scan() {
		return "", "", fmt.Errorf("file is empty or cannot be read")
	}

	firstLine := strings.TrimSpace(scanner.Text())
	if firstLine != "---" {
		return "", "", fmt.Errorf("file does not start with --- (found: %q)", firstLine)
	}

	var frontmatterLines []string
	foundEnd := false

	// Read until we find the closing ---
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			foundEnd = true
			break
		}
		frontmatterLines = append(frontmatterLines, line)
	}

	if !foundEnd {
		return "", "", fmt.Errorf("frontmatter not properly closed (missing closing ---)")
	}

	// Read remaining content
	var contentLines []string
	for scanner.Scan() {
		contentLines = append(contentLines, scanner.Text())
	}

	frontmatter := strings.Join(frontmatterLines, "\n")
	content := strings.Join(contentLines, "\n")

	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("error reading file: %v", err)
	}

	return content, frontmatter, nil
}

func validateFrontmatter(filePath string, frontmatter string) []ValidationError {
	var errors []ValidationError

	// Parse YAML
	var data map[string]interface{}
	err := yaml.Unmarshal([]byte(frontmatter), &data)
	if err != nil {
		errors = append(errors, ValidationError{
			File:    filePath,
			Message: fmt.Sprintf("invalid YAML frontmatter: %v", err),
		})
		return errors
	}

	// Check required fields are present
	for _, field := range RequiredFields {
		if _, exists := data[field]; !exists {
			errors = append(errors, ValidationError{
				File:    filePath,
				Message: fmt.Sprintf("missing required field: %q", field),
			})
		}
	}

	// Check no extra fields are present
	for key := range data {
		found := false
		for _, allowed := range AllowedFields {
			if key == allowed {
				found = true
				break
			}
		}
		if !found {
			errors = append(errors, ValidationError{
				File:    filePath,
				Message: fmt.Sprintf("unexpected field: %q (allowed: %v)", key, AllowedFields),
			})
		}
	}

	// Check field order (by analyzing the raw YAML text)
	orderErrors := validateFieldOrder(filePath, frontmatter)
	errors = append(errors, orderErrors...)

	return errors
}

func validateFieldOrder(filePath string, frontmatter string) []ValidationError {
	var errors []ValidationError

	// Extract field positions from YAML
	re := regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_-]*):\s`)

	lines := strings.Split(frontmatter, "\n")
	lastTopLevelField := ""

	for i, line := range lines {
		// Count leading spaces to determine nesting level
		leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
		isTopLevel := leadingSpaces == 0

		matches := re.FindStringSubmatch(line)
		if len(matches) > 1 {
			field := matches[1]

			if isTopLevel {
				// Track top-level field order
				if lastTopLevelField != "" {
					// Check order
					lastAllowed := -1
					currentAllowed := -1
					for j, allowed := range RequiredFields {
						if allowed == lastTopLevelField {
							lastAllowed = j
						}
						if allowed == field {
							currentAllowed = j
						}
					}

					// Also check in AllowedFields for optional fields
					if currentAllowed == -1 {
						for j, allowed := range AllowedFields {
							if allowed == field {
								currentAllowed = j
							}
						}
					}
					if lastAllowed == -1 {
						for j, allowed := range AllowedFields {
							if allowed == lastTopLevelField {
								lastAllowed = j
							}
						}
					}

					if lastAllowed != -1 && currentAllowed != -1 && currentAllowed < lastAllowed {
						errors = append(errors, ValidationError{
							File:    filePath,
							Message: fmt.Sprintf("field %q (line %d) appears after %q but should come before it (expected order: %v)", field, i+1, lastTopLevelField, AllowedFields),
						})
					}
				}

				lastTopLevelField = field
			}
		}
	}

	return errors
}
