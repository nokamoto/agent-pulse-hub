package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const validFrontmatter = `type: Design
title: Example design
description: Describes the selected approach.
sources:
  - id: REQ-1
    resource: requirements/example.md
`

func document(frontmatter, body string) string {
	return "---\n" + frontmatter + "---\n" + body
}

func TestValidateDocument(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"valid", document(validFrontmatter, "# Approach\nContent."), ""},
		{"body has no prescribed headings", document(validFrontmatter, "An explanation."), ""},
		{"external source", document(strings.ReplaceAll(validFrontmatter, "requirements/example.md", "https://example.com/reference"), "Body"), ""},
		{"optional source title", document(validFrontmatter+"    title: Reference title\n", "Body"), ""},
		{"empty optional source title", document(validFrontmatter+"    title: \"\"\n", "Body"), ""},
		{"quoted keys", document(strings.Replace(validFrontmatter, "type:", "\"type\":", 1), "Body"), ""},
		{"flow mapping", document("{type: Design, title: Example, description: Explanation, sources: [{id: REF, resource: ref.md}]}\n", "Body"), ""},
		{"CRLF", strings.ReplaceAll(document(validFrontmatter, "Body"), "\n", "\r\n"), ""},
		{"large body", document(validFrontmatter, strings.Repeat("x", 128*1024)), ""},
		{"empty file", "", "must start"},
		{"invalid UTF-8 frontmatter", document("title: \xff\n", "Body"), "valid UTF-8"},
		{"invalid UTF-8 body", document(validFrontmatter, "Body\xff"), "valid UTF-8"},
		{"no opening delimiter", "# Approach", "must start"},
		{"no closing delimiter", "---\n" + validFrontmatter, "missing its closing"},
		{"empty body", document(validFrontmatter, ""), "body must not be empty"},
		{"whitespace body", document(validFrontmatter, "\n \t\n"), "body must not be empty"},
		{"empty frontmatter", document("", "Body"), "invalid YAML"},
		{"malformed YAML", document("type: [\n", "Body"), "invalid YAML"},
		{"malformed YAML document boundary", document(validFrontmatter+"...\n{}\n", "Body"), "invalid YAML"},
		{"sequence frontmatter", document("- item\n", "Body"), "must be a mapping"},
		{"scalar frontmatter", document("Design\n", "Body"), "must be a mapping"},
		{"null frontmatter", document("null\n", "Body"), "must be a mapping"},
		{"missing type", document(strings.Replace(validFrontmatter, "type: Design\n", "", 1), "Body"), "missing required field \"type\""},
		{"missing sources", document("type: Design\ntitle: Example\ndescription: Explanation\n", "Body"), "missing required field \"sources\""},
		{"wrong type value", document(strings.Replace(validFrontmatter, "type: Design", "type: Requirement", 1), "Body"), "must equal \"Design\""},
		{"empty title", document(strings.Replace(validFrontmatter, "title: Example design", "title: ' '", 1), "Body"), "field \"title\" must be a non-empty string"},
		{"numeric title", document(strings.Replace(validFrontmatter, "title: Example design", "title: 123", 1), "Body"), "field \"title\" must be a non-empty string"},
		{"null description", document(strings.Replace(validFrontmatter, "description: Describes the selected approach.", "description: null", 1), "Body"), "field \"description\" must be a non-empty string"},
		{"mapping description", document(strings.Replace(validFrontmatter, "description: Describes the selected approach.", "description: {text: Explanation}", 1), "Body"), "field \"description\" must be a non-empty string"},
		{"wrong order", document(strings.Replace(validFrontmatter, "type: Design\ntitle: Example design", "title: Example design\ntype: Design", 1), "Body"), "out of order"},
		{"sources in wrong order", document("sources: [{id: REF, resource: ref.md}]\ntype: Design\ntitle: Example\ndescription: Explanation\n", "Body"), "out of order"},
		{"unexpected field", document(validFrontmatter+"status: Draft\n", "Body"), "unexpected field \"status\""},
		{"duplicate field", document(validFrontmatter+"title: Duplicate\n", "Body"), "duplicate field \"title\""},
		{"nonstring key", document(validFrontmatter+"42: ignored\n", "Body"), "field names must be strings"},
		{"empty source list", document("type: Design\ntitle: Example\ndescription: Explanation\nsources: []\n", "Body"), "must be a non-empty list"},
		{"source mapping instead of list", document("type: Design\ntitle: Example\ndescription: Explanation\nsources: {id: REF, resource: ref.md}\n", "Body"), "must be a non-empty list"},
		{"scalar source", document("type: Design\ntitle: Example\ndescription: Explanation\nsources: [ref.md]\n", "Body"), "sources[0] must be a mapping"},
		{"missing source id", document(strings.Replace(validFrontmatter, "  - id: REQ-1\n    resource:", "  - resource:", 1), "Body"), "sources[0] is missing required field \"id\""},
		{"empty resource", document(strings.Replace(validFrontmatter, "resource: requirements/example.md", "resource: ''", 1), "Body"), "sources[0].resource must be a non-empty string"},
		{"numeric source id", document(strings.Replace(validFrontmatter, "id: REQ-1", "id: 1", 1), "Body"), "sources[0].id must be a non-empty string"},
		{"unexpected source field", document(validFrontmatter+"    author: Someone\n", "Body"), "unexpected field \"author\" in sources[0]"},
		{"duplicate source field", document(validFrontmatter+"    id: Duplicate\n", "Body"), "duplicate field \"id\" in sources[0]"},
		{"nonstr source title", document(validFrontmatter+"    title: false\n", "Body"), "sources[0].title must be a string"},
		{"alias source", document("type: Design\ntitle: Example\ndescription: Explanation\nsources:\n  - &source {id: REF, resource: ref.md}\n  - *source\n", "Body"), "sources[1] must be a mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errors := validateDocument("example.md", tt.content)
			if tt.want == "" {
				if len(errors) != 0 {
					t.Fatalf("valid document rejected: %v", errors)
				}
				return
			}
			for _, err := range errors {
				if err.File != "example.md" {
					t.Fatalf("diagnostic file = %q; want example.md", err.File)
				}
				if strings.Contains(err.Message, tt.want) {
					return
				}
			}
			t.Fatalf("errors = %v; want diagnostic containing %q", errors, tt.want)
		})
	}
}

func TestDiagnosticLine(t *testing.T) {
	errors := validateDocument("example.md", document(strings.Replace(validFrontmatter, "title: Example design", "title: 123", 1), "Body"))
	if len(errors) != 1 || !strings.HasPrefix(errors[0].Message, "line 3:") {
		t.Fatalf("errors = %v; want title diagnostic at file line 3", errors)
	}
}

func TestRejectMultipleYAMLDocuments(t *testing.T) {
	errors := validateFrontmatter("example.md", validFrontmatter+"---\n{}\n")
	if len(errors) != 1 || !strings.Contains(errors[0].Message, "exactly one YAML document") {
		t.Fatalf("errors = %v; want multiple YAML document rejection", errors)
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRepository(t *testing.T) {
	root := t.TempDir()
	template := filepath.Join(root, filepath.FromSlash(templatePath))
	errors := validateRepository(root)
	if len(errors) != 1 || errors[0].File != template {
		t.Fatalf("errors = %v; want missing mandatory template", errors)
	}
	writeFixture(t, template, document(validFrontmatter, "Template body"))
	if errors := validateRepository(root); len(errors) != 0 {
		t.Fatalf("absent optional design directory rejected: %v", errors)
	}
	dir := filepath.Join(root, "docs", "design")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if errors := validateRepository(root); len(errors) != 0 {
		t.Fatalf("empty design directory rejected: %v", errors)
	}
	writeFixture(t, filepath.Join(dir, "valid.md"), document(validFrontmatter, "Valid body"))
	writeFixture(t, filepath.Join(dir, "notes.txt"), "Not a design document")
	nested := filepath.Join(dir, "nested", "invalid.md")
	writeFixture(t, nested, document(validFrontmatter, ""))
	errors = validateRepository(root)
	if len(errors) != 1 || errors[0].File != nested || !strings.Contains(errors[0].Message, "body must not be empty") {
		t.Fatalf("errors = %v; want invalid nested Markdown diagnostic", errors)
	}
}

func TestRejectNonDirectoryRootAndNonRegularFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "design")
	writeFixture(t, file, "not a directory")
	if errors := validateDesignDir(file); len(errors) != 1 || !strings.Contains(errors[0].Message, "must be a directory") {
		t.Fatalf("errors = %v; want directory type error", errors)
	}
	dir := filepath.Join(root, "directory.md")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if errors := validateFile(dir); len(errors) != 1 || !strings.Contains(errors[0].Message, "must be a regular file") {
		t.Fatalf("errors = %v; want regular file error", errors)
	}
}

func TestRejectSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.md")
	writeFixture(t, target, document(validFrontmatter, "Body"))
	dir := filepath.Join(root, "design")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if errors := validateFile(link); len(errors) != 1 || !strings.Contains(errors[0].Message, "symlink") {
		t.Fatalf("errors = %v; want file symlink rejection", errors)
	}
	if errors := validateDesignDir(dir); len(errors) != 1 || errors[0].File != link || !strings.Contains(errors[0].Message, "symlinks") {
		t.Fatalf("errors = %v; want walk symlink rejection", errors)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if errors := validateDesignDir(dir); len(errors) != 1 || !strings.Contains(errors[0].Message, "symlinks") {
		t.Fatalf("errors = %v; want dangling symlink rejection", errors)
	}
	dirLink := filepath.Join(root, "linked-directory")
	if err := os.Symlink(dir, dirLink); err != nil {
		t.Fatal(err)
	}
	if errors := validateDesignDir(dirLink); len(errors) != 1 || !strings.Contains(errors[0].Message, "symlinks") {
		t.Fatalf("errors = %v; want directory symlink rejection", errors)
	}
}

func TestUnreadableNestedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode permissions are not enforced on Windows")
	}
	dir := t.TempDir()
	nested := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(nested, 0o755); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.ReadDir(nested); err == nil {
		t.Skip("current user can read directories without permission bits")
	}
	errors := validateDesignDir(dir)
	if len(errors) != 1 || errors[0].File != nested || !strings.Contains(errors[0].Message, "cannot walk design directory") {
		t.Fatalf("errors = %v; want recursive walk error", errors)
	}
}
