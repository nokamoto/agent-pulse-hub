package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/net/html"
)

var requiredSections = []string{
	"Background and motivation",
	"Requirements",
	"Installation",
	"Usage",
	"Writing plugins",
}

var atxDelimiter = regexp.MustCompile(`^#{1,6}(?:[\t \r\n]|$)`)

func main() {
	if err := validateRepository("."); err != nil {
		fmt.Fprintf(os.Stderr, "README structure validation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("README.md follows the required user-facing structure")
}

func validateRepository(root string) error {
	path := filepath.Join(root, "README.md")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("README.md: cannot inspect required file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("README.md: must be a regular file; symlinks and special files are not allowed")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("README.md: cannot read required file: %w", err)
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("README.md: must contain valid UTF-8")
	}
	return validateContent(string(content))
}

func validateContent(content string) error {
	source := []byte(content)
	document := goldmark.New().Parser().Parse(text.NewReader(source))
	titleSeen, sectionCount := false, 0
	fail := func(position int, message string, args ...any) error {
		line := bytes.Count(source[:position], []byte{'\n'}) + 1
		return fmt.Errorf("README.md:%d: %s", line, fmt.Sprintf(message, args...))
	}
	err := ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var rawHTML []byte
		switch node := node.(type) {
		case *ast.HTMLBlock:
			rawHTML = node.Lines().Value(source)
			if node.HasClosure() {
				rawHTML = append(rawHTML, node.ClosureLine.Value(source)...)
			}
		case *ast.RawHTML:
			rawHTML = node.Segments.Value(source)
		}
		if rawHTML != nil {
			if err := validateHTML(rawHTML); err != nil {
				return ast.WalkStop, fail(node.Pos(), "%v", err)
			}
		}
		heading, ok := node.(*ast.Heading)
		if !ok {
			return ast.WalkContinue, nil
		}
		if !atxDelimiter.Match(source[heading.Pos():]) {
			return ast.WalkStop, fail(heading.Pos(), "setext headings are not allowed; use ATX headings outside lists and blockquotes")
		}
		if heading.Parent() != document {
			return ast.WalkStop, fail(heading.Pos(), "headings inside lists or blockquotes are not allowed")
		}
		title := strings.TrimSpace(string(heading.Lines().Value(source)))
		switch heading.Level {
		case 1:
			if title != "agent-pulse-hub" {
				return ast.WalkStop, fail(heading.Pos(), "H1 must be exactly %q, got %q", "agent-pulse-hub", title)
			}
			if titleSeen {
				return ast.WalkStop, fail(heading.Pos(), "duplicate H1; exactly one # agent-pulse-hub is required")
			}
			titleSeen = true
		case 2:
			if !titleSeen {
				return ast.WalkStop, fail(heading.Pos(), "# agent-pulse-hub must appear before all H2 sections")
			}
			if sectionCount == len(requiredSections) {
				return ast.WalkStop, fail(heading.Pos(), "unexpected H2 %q; only the five required sections are allowed", title)
			}
			if title != requiredSections[sectionCount] {
				return ast.WalkStop, fail(heading.Pos(), "expected H2 %q, got %q; required sections must appear exactly once in order", requiredSections[sectionCount], title)
			}
			sectionCount++
		default:
			if sectionCount == 0 {
				return ast.WalkStop, fail(heading.Pos(), "H3-H6 subsections must appear within a required H2 section")
			}
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return err
	}
	if !titleSeen {
		return fail(len(source), "missing required H1 # agent-pulse-hub")
	}
	if sectionCount != len(requiredSections) {
		return fail(len(source), "missing required H2 %q", requiredSections[sectionCount])
	}
	return nil
}

// The HTML tokenizer ignores comments, quoted attributes, and script text, so
// HTML examples in those contexts do not masquerade as document headings.
func validateHTML(content []byte) error {
	tokenizer := html.NewTokenizer(bytes.NewReader(content))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != io.EOF {
				return fmt.Errorf("cannot parse HTML: %w", err)
			}
			return nil
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
				return fmt.Errorf("HTML headings are not allowed; use ATX headings outside lists and blockquotes")
			}
		}
	}
}
