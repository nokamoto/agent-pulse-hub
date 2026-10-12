package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validREADME = "# agent-pulse-hub\n\n" +
	"## Background and motivation\n\n" +
	"## Requirements\n\n" +
	"## Installation\n\n" +
	"## Usage\n\n" +
	"## Writing plugins\n"

func TestValidateContent(t *testing.T) {
	addToSection := func(text string) string {
		return strings.Replace(validREADME, "## Requirements", text+"\n## Requirements", 1)
	}
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty section bodies", validREADME, ""},
		{"introductory prose", strings.Replace(validREADME, "\n\n", "\n\nAn introduction with new wording.\n\n", 1), ""},
		{"free prose changes", addToSection("Different paragraphs, punctuation, and links [here](https://example.com)."), ""},
		{"arbitrary subsections", addToSection("### Explanation\n#### More detail\n##### Detail\n###### Detail\n### Repeated\n### Repeated"), ""},
		{"last section subsection", validREADME + "### References\n", ""},
		{"empty subsection", addToSection("###"), ""},
		{"atx indentation and closing markers", strings.ReplaceAll(strings.ReplaceAll(validREADME, "\n## ", "\n   ## "), "\n\n", " ##\n\n"), ""},
		{"heading tabs and trailing spaces", strings.ReplaceAll(validREADME, "# ", "#\t") + " \n", ""},
		{"CRLF", strings.ReplaceAll(validREADME, "\n", "\r\n"), ""},
		{"ordinary list and quote prose", addToSection("- A list item\n  Its continuation\n1. Another item\n> A quotation\n> Continued text\n\n---\n\n***"), ""},
		{"indented code", addToSection("    ## Unknown\n    <h2>Unknown</h2>\n    ==="), ""},
		{"tab indented code", addToSection("\t## Unknown"), ""},
		{"backtick fenced examples", addToSection("```markdown\n# Other\n## Unknown\ntext\n===\n<h2>Unknown</h2>\n```"), ""},
		{"tilde fenced examples", addToSection("~~~markdown\n## Unknown\n~~~"), ""},
		{"long fence with short markers inside", addToSection("````markdown\n```\n## Unknown\n~~~\n`````"), ""},
		{"long tilde fence", addToSection("~~~~markdown\n~~~\n## Unknown\n~~~~~"), ""},
		{"closing fence must have no info string", addToSection("```\n```not-a-close\n## Unknown\n```"), ""},
		{"indent fenced examples", addToSection("   ```\n## Unknown\n  ```"), ""},
		{"quoted fenced examples", addToSection("> ```markdown\n> ## Unknown\n> ```"), ""},
		{"list fenced examples", addToSection("- ```markdown\n  ## Unknown\n  ```"), ""},
		{"nested container fenced examples", addToSection("- > ```\n  > ## Unknown\n  > ```"), ""},
		{"list indented code", addToSection("- Item\n\n      ## Unknown\n      <h2>Unknown</h2>"), ""},
		{"inline HTML code examples", addToSection("Use `<h2>Heading</h2>` or `` `<h3>` `` as examples."), ""},
		{"escaped HTML examples", addToSection(`Use \<h2>Heading\</h2> as escaped text.`), ""},
		{"literal hashes", addToSection("#Not a heading\n####### Not a heading\n\\## Not a heading"), ""},
		{"missing title", strings.TrimPrefix(validREADME, "# agent-pulse-hub\n\n"), "must appear before"},
		{"empty file", "", "missing required H1"},
		{"wrong title", strings.Replace(validREADME, "agent-pulse-hub", "Other project", 1), "H1 must be exactly"},
		{"duplicate title", validREADME + "# agent-pulse-hub\n", "duplicate H1"},
		{"title after section", strings.Replace(validREADME, "# agent-pulse-hub\n\n## Background and motivation", "## Background and motivation\n\n# agent-pulse-hub", 1), "must appear before"},
		{"missing middle section", strings.Replace(validREADME, "## Installation\n\n", "", 1), "expected H2 \"Installation\""},
		{"missing final section", strings.Replace(validREADME, "## Writing plugins\n", "", 1), "missing required H2 \"Writing plugins\""},
		{"unknown middle section", addToSection("## Unknown"), "expected H2 \"Requirements\""},
		{"unknown final section", validREADME + "## Unknown\n", "unexpected H2 \"Unknown\""},
		{"case sensitive section", strings.Replace(validREADME, "## Usage", "## usage", 1), "expected H2 \"Usage\""},
		{"duplicate section", addToSection("## Background and motivation"), "expected H2 \"Requirements\""},
		{"wrong order", strings.Replace(validREADME, "## Requirements\n\n## Installation", "## Installation\n\n## Requirements", 1), "expected H2 \"Requirements\""},
		{"subsection before first section", strings.Replace(validREADME, "## Background", "### Early\n## Background", 1), "must appear within"},
		{"subsection before title", "### Early\n" + validREADME, "must appear within"},
		{"setext equals", addToSection("Unknown\n==="), "setext headings are not allowed"},
		{"setext dash", addToSection("Unknown\n---"), "setext headings are not allowed"},
		{"single dash setext", addToSection("Unknown\n-"), "setext headings are not allowed"},
		{"indented setext", addToSection("Unknown\n   ---"), "setext headings are not allowed"},
		{"quoted setext", addToSection("> Unknown\n> ==="), "setext headings are not allowed"},
		{"list setext", addToSection("- Unknown\n  ==="), "setext headings are not allowed"},
		{"HTML heading", addToSection("<h2>Unknown</h2>"), "HTML headings are not allowed"},
		{"uppercase HTML heading", addToSection("<H6 id=\"other\">Unknown</H6>"), "HTML headings are not allowed"},
		{"multiline HTML tag", addToSection("<h2\nid=\"other\">Unknown</h2>"), "HTML headings are not allowed"},
		{"inline raw HTML heading", addToSection("Text <h2>Unknown</h2>"), "HTML headings are not allowed"},
		{"escaped backtick cannot hide HTML", addToSection("\\`<h2>Unknown</h2>`"), "HTML headings are not allowed"},
		{"even backslashes cannot hide HTML", addToSection(`\\<h2>Unknown</h2>`), "HTML headings are not allowed"},
		{"quoted heading", addToSection("> ## Unknown"), "headings inside lists or blockquotes"},
		{"nested quote heading", addToSection("> > ### Unknown"), "headings inside lists or blockquotes"},
		{"list heading", addToSection("- ## Unknown"), "headings inside lists or blockquotes"},
		{"ordered list heading", addToSection("12) ### Unknown"), "headings inside lists or blockquotes"},
		{"list continuation heading", addToSection("- Item\n  ## Unknown"), "headings inside lists or blockquotes"},
		{"list wide continuation heading", addToSection("1. Item\n    ## Unknown"), "headings inside lists or blockquotes"},
		{"nested list continuation heading", addToSection("- Item\n  - Nested\n    ## Unknown"), "headings inside lists or blockquotes"},
		{"quote list heading", addToSection("> - ### Unknown"), "headings inside lists or blockquotes"},
		{"unclosed fence after required headings is code", validREADME + "```\n## Unknown\n", ""},
		{"unclosed fence hides required headings", "```\n" + validREADME, "missing required H1"},
		{"heading after closed fence", validREADME + "```\n## Example\n```\n## Unknown\n", "unexpected H2 \"Unknown\""},
		{"quote cannot close root fence", validREADME + "```\n> ```\n", ""},
		{"indented fence is code", validREADME + "    ```\n## Unknown\n", "unexpected H2 \"Unknown\""},
		{"ending quote does not hide heading", validREADME + "> ```\n> Example\n## Unknown\n", "unexpected H2 \"Unknown\""},
		{"empty list item heading", addToSection("-\n  ## Unknown"), "headings inside lists or blockquotes"},
		{"empty ordered item heading", addToSection("1.\n   ## Unknown"), "headings inside lists or blockquotes"},
		{"lazy list continuation heading", addToSection("- Item\ncontinuation\n  ## Unknown"), "headings inside lists or blockquotes"},
		{"all headings inside comment", "<!--\n" + validREADME + "-->\n", "missing required H1"},
		{"extra heading inside comment", validREADME + "<!--\n## Extra\n<h2>Extra</h2>\n-->\n", ""},
		{"inline HTML comment heading example", addToSection("Text <!-- <h2>Extra</h2> --> text"), ""},
		{"HTML attributes are not headings", addToSection("<div title=\"<h2>Example\">Text</div>\n"), ""},
		{"HTML script text is not a heading", addToSection("<script>var example = '<h2>Example</h2>';</script>\n"), ""},
		{"HTML heading after block comment", addToSection("<!-- comment --><h2>Unknown</h2>\n"), "HTML headings are not allowed"},
		{"setext text starts with literal hash", addToSection("#Not a heading\n---"), "setext headings are not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateContent(tt.content)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("valid README rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "README.md:") {
				t.Fatalf("error = %v; want README.md error containing %q", err, tt.want)
			}
		})
	}
}

func TestDiagnosticsIncludeLine(t *testing.T) {
	err := validateContent("# agent-pulse-hub\n\n## Unknown\n")
	if err == nil || !strings.HasPrefix(err.Error(), "README.md:3:") {
		t.Fatalf("error = %v; want README.md:3 diagnostic", err)
	}
}

func TestValidateRepository(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		want    string
	}{
		{"valid", []byte(validREADME), ""},
		{"invalid structure", []byte("# Other\n"), "H1 must be exactly"},
		{"invalid UTF-8", []byte{0xff}, "valid UTF-8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "README.md"), test.content, 0o644); err != nil {
				t.Fatal(err)
			}
			// Nested README files are outside the guardrail's scope.
			if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "docs", "README.md"), []byte("# Other"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := validateRepository(root)
			if test.want == "" && err != nil {
				t.Fatal(err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestRequiredREADMEFilesystemErrors(t *testing.T) {
	root := t.TempDir()
	if err := validateRepository(root); err == nil || !strings.Contains(err.Error(), "cannot inspect required file") {
		t.Fatalf("missing README error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "README.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateRepository(root); err == nil || !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("directory README error = %v", err)
	}
}

func TestRejectSymlinkREADME(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.md")
	if err := os.WriteFile(target, []byte(validREADME), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "README.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateRepository(root); err == nil || !strings.Contains(err.Error(), "symlinks and special files are not allowed") {
		t.Fatalf("symlink README error = %v", err)
	}
}
