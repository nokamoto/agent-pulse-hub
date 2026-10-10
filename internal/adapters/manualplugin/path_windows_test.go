//go:build windows

package manualplugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTriggerPathCanonicalizesLocalComponents(t *testing.T) {
	path, parent, name, err := normalizeTriggerPath(`C:/events/../tmp/next.txt`)
	if err != nil {
		t.Fatal(err)
	}
	if path != `C:\tmp\next.txt` || parent != `C:\tmp` || name != "next.txt" {
		t.Fatalf("normalizeTriggerPath() = %q, %q, %q", path, parent, name)
	}
}

func TestNormalizeTriggerPathRejectsUnsupportedForms(t *testing.T) {
	for _, input := range []string{
		`\\server\share\next.txt`,
		`C:\..\next.txt`,
		`C:\tmp\next.txt:stream`,
		`C:\tmp\`,
		`C:\tmp\name.`,
	} {
		if _, _, _, err := normalizeTriggerPath(input); err == nil {
			t.Errorf("normalizeTriggerPath(%q) succeeded", input)
		}
	}
}

func TestConsumeTriggerClaimsReadsAndRemovesFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "next.txt")
	target, err := parseWatchTarget(path)
	if err != nil {
		t.Fatal(err)
	}
	const contextText = "PULSE-DEMO-001"
	if err := os.WriteFile(path, []byte(contextText), 0o600); err != nil {
		t.Fatal(err)
	}
	got, claimed, err := consumeTrigger(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != contextText || claimed == "" {
		t.Fatalf("consumeTrigger() = %q, %q; want context and claimed path", got, claimed)
	}
	for _, path := range []string{path, claimed} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("path %q remains or cannot be checked: %v", path, err)
		}
	}
}

func TestManualWatchTargetComparisonUsesParentIdentityAndOrdinalName(t *testing.T) {
	directory := t.TempDir()
	first, err := parseWatchTarget(filepath.Join(directory, "next.txt"))
	if err != nil {
		t.Fatal(err)
	}
	secondPath := strings.ToUpper(directory) + `\NEXT.TXT`
	second, err := parseWatchTarget(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTarget(first.key, second.key) {
		t.Fatal("case variants of the same directory entry were treated as different targets")
	}
}
