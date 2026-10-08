package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const templatePath = ".agents/skills/create-design/assets/design.md"

var requiredFields = []string{"type", "title", "description", "sources"}

type validationError struct {
	File    string
	Message string
}

func main() {
	errors := validateRepository(".")
	if len(errors) > 0 {
		fmt.Fprintln(os.Stderr, "Design validation failed:")
		for _, err := range errors {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", err.File, err.Message)
		}
		os.Exit(1)
	}
	fmt.Println("All design files are valid")
}

func validateRepository(root string) []validationError {
	errors := validateDesignDir(filepath.Join(root, "docs", "design"))
	return append(errors, validateFile(filepath.Join(root, filepath.FromSlash(templatePath)))...)
}

func validateDesignDir(dir string) []validationError {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return []validationError{{dir, fmt.Sprintf("cannot inspect design directory: %v", err)}}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []validationError{{dir, "symlinks are not allowed in the design directory"}}
	}
	if !info.IsDir() {
		return []validationError{{dir, "design directory must be a directory"}}
	}

	var errors []validationError
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			errors = append(errors, validationError{path, fmt.Sprintf("cannot walk design directory: %v", walkErr)})
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			errors = append(errors, validationError{path, "symlinks are not allowed in the design directory"})
			return nil
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			errors = append(errors, validateFile(path)...)
		}
		return nil
	})
	if err != nil {
		errors = append(errors, validationError{dir, fmt.Sprintf("cannot walk design directory: %v", err)})
	}
	return errors
}

func validateFile(path string) []validationError {
	info, err := os.Lstat(path)
	if err != nil {
		return []validationError{{path, fmt.Sprintf("cannot inspect required design file: %v", err)}}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []validationError{{path, "design file must not be a symlink"}}
	}
	if !info.Mode().IsRegular() {
		return []validationError{{path, "design file must be a regular file"}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []validationError{{path, fmt.Sprintf("cannot read design file: %v", err)}}
	}
	return validateDocument(path, string(data))
}

func validateDocument(path, content string) []validationError {
	if !utf8.ValidString(content) {
		return []validationError{{path, "design file must contain valid UTF-8"}}
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return []validationError{{path, "file must start with YAML frontmatter delimited by ---"}}
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return []validationError{{path, "frontmatter is missing its closing --- delimiter"}}
	}
	errors := validateFrontmatter(path, strings.Join(lines[1:end], "\n"))
	if strings.TrimSpace(strings.Join(lines[end+1:], "\n")) == "" {
		errors = append(errors, validationError{path, "Markdown body must not be empty"})
	}
	return errors
}

func validateFrontmatter(path, frontmatter string) []validationError {
	decoder := yaml.NewDecoder(strings.NewReader(frontmatter))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return []validationError{{path, fmt.Sprintf("invalid YAML frontmatter: %v", err)}}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return []validationError{{path, fmt.Sprintf("invalid YAML frontmatter: %v", err)}}
		}
		return []validationError{{path, "frontmatter must contain exactly one YAML document"}}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return []validationError{{path, "YAML frontmatter must be a mapping"}}
	}
	root := document.Content[0]
	fields, errors := mappingFields(path, root, "frontmatter", requiredFields)
	lastIndex := -1
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		for index, field := range requiredFields {
			if key.Value == field {
				if index < lastIndex {
					errors = append(errors, nodeError(path, key, fmt.Sprintf("field %q is out of order; expected: type, title, description, sources", field)))
				}
				lastIndex = index
			}
		}
	}
	for _, name := range requiredFields {
		value, exists := fields[name]
		if !exists {
			errors = append(errors, validationError{path, fmt.Sprintf("missing required field %q", name)})
			continue
		}
		if name == "sources" {
			errors = append(errors, validateSources(path, value)...)
			continue
		}
		if !nonemptyString(value) {
			errors = append(errors, nodeError(path, value, fmt.Sprintf("field %q must be a non-empty string", name)))
		} else if name == "type" && value.Value != "Design" {
			errors = append(errors, nodeError(path, value, "field \"type\" must equal \"Design\""))
		}
	}
	return errors
}

func validateSources(path string, sources *yaml.Node) []validationError {
	if sources.Kind != yaml.SequenceNode || len(sources.Content) == 0 {
		return []validationError{nodeError(path, sources, "field \"sources\" must be a non-empty list")}
	}
	var errors []validationError
	for i, source := range sources.Content {
		label := fmt.Sprintf("sources[%d]", i)
		if source.Kind != yaml.MappingNode {
			errors = append(errors, nodeError(path, source, label+" must be a mapping"))
			continue
		}
		fields, mappingErrors := mappingFields(path, source, label, []string{"id", "resource", "title"})
		errors = append(errors, mappingErrors...)
		for _, name := range []string{"id", "resource"} {
			value, exists := fields[name]
			if !exists {
				errors = append(errors, nodeError(path, source, fmt.Sprintf("%s is missing required field %q", label, name)))
			} else if !nonemptyString(value) {
				errors = append(errors, nodeError(path, value, fmt.Sprintf("%s.%s must be a non-empty string", label, name)))
			}
		}
		if title, exists := fields["title"]; exists && (title.Kind != yaml.ScalarNode || title.Tag != "!!str") {
			errors = append(errors, nodeError(path, title, label+".title must be a string when present"))
		}
	}
	return errors
}

func mappingFields(path string, mapping *yaml.Node, label string, allowed []string) (map[string]*yaml.Node, []validationError) {
	fields := make(map[string]*yaml.Node)
	var errors []validationError
	for i := 0; i < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			errors = append(errors, nodeError(path, key, label+" field names must be strings"))
			continue
		}
		if _, exists := fields[key.Value]; exists {
			errors = append(errors, nodeError(path, key, fmt.Sprintf("duplicate field %q in %s", key.Value, label)))
			continue
		}
		fields[key.Value] = value
		known := false
		for _, name := range allowed {
			if key.Value == name {
				known = true
				break
			}
		}
		if !known {
			errors = append(errors, nodeError(path, key, fmt.Sprintf("unexpected field %q in %s; allowed: %s", key.Value, label, strings.Join(allowed, ", "))))
		}
	}
	return fields, errors
}

func nonemptyString(node *yaml.Node) bool {
	return node.Kind == yaml.ScalarNode && node.Tag == "!!str" && strings.TrimSpace(node.Value) != ""
}

func nodeError(path string, node *yaml.Node, message string) validationError {
	return validationError{path, fmt.Sprintf("line %d: %s", node.Line+1, message)}
}
