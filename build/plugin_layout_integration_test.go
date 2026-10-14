//go:build mage && integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Qualify actual discovery and the scoped runner on a temporary root module.
// Product commands and their existing acceptance inventory remain untouched.
func qualifyPluginLayout(t *testing.T, sourceRoot, outputDir string) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile(filepath.Join(sourceRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		writeLayoutFixture(t, root, name, data)
	}
	fixture, err := os.ReadFile(filepath.Join(sourceRoot, "build/testdata/plugin-layout/suite_integration_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"cmd/example", "plugins/triggers/example", "plugins/actions/example"}
	var inventory acceptanceInventory
	for i, path := range paths {
		id := []string{"CMD", "TRIGGER", "ACTION"}[i]
		data := bytes.ReplaceAll(fixture, []byte("LAYOUT-"), []byte(id+"-"))
		writeLayoutFixture(t, root, path+"/suite_integration_test.go", data)
		inventory.Suites = append(inventory.Suites, acceptanceSuite{Package: "./" + path, Cases: []string{id + "-PASS"}})
	}
	// A normal internal package is not a command or plugin suite.
	writeLayoutFixture(t, root, "internal/domain/model.go", []byte("package domain\n"))
	packages, err := discoverIntegrationPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./cmd/example", "./plugins/actions/example", "./plugins/triggers/example"}
	if !slices.Equal(packages, want) {
		t.Fatalf("discovered %v; want %v", packages, want)
	}
	var output bytes.Buffer
	generalDir := filepath.Join(outputDir, "all")
	if err := runIntegrationAll(root, generalDir, &output, &output); err != nil {
		t.Fatalf("automatic discovery run: %v\n%s", err, output.String())
	}
	results, err := os.ReadFile(filepath.Join(generalDir, "test-results.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !bytes.Contains(results, []byte("agent-pulse-hub/"+path)) {
			t.Fatalf("missing execution evidence for %s", path)
		}
	}
	if !bytes.Contains(results, []byte("1 Pending")) {
		t.Fatal("automatic run dropped static Pending")
	}
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	writeLayoutFixture(t, root, "cases.json", data)
	if err := runIntegration(root, "cases.json", filepath.Join(outputDir, "accepted"), &output, &output); err != nil {
		t.Fatalf("mixed command/plugin acceptance: %v\n%s", err, output.String())
	}
	// An action failure must fail the general run and the acceptance gate.
	failing := bytes.ReplaceAll(fixture, []byte("LAYOUT-"), []byte("ACTION-"))
	failing = bytes.ReplaceAll(failing, []byte("Expect(true)"), []byte("Expect(false)"))
	writeLayoutFixture(t, root, "plugins/actions/example/suite_integration_test.go", failing)
	if err := runIntegrationAll(root, filepath.Join(outputDir, "failed-all"), &output, &output); err == nil {
		t.Fatal("automatic run accepted failing plugin")
	}
	if err := runIntegration(root, "cases.json", filepath.Join(outputDir, "failed-acceptance"), &output, &output); err == nil {
		t.Fatal("scoped gate accepted failing plugin")
	}
	if err := os.Remove(filepath.Join(root, "plugins/actions/example/suite_integration_test.go")); err != nil {
		t.Fatal(err)
	}
	if err := runIntegration(root, "cases.json", filepath.Join(outputDir, "missing-acceptance"), &output, &output); err == nil {
		t.Fatal("scoped gate accepted missing plugin suite")
	}
	writeLayoutFixture(t, root, "plugins/actions/example/broken.go", []byte("invalid Go"))
	if _, err := discoverIntegrationPackages(root); err == nil || !strings.Contains(err.Error(), "discover integration packages") {
		t.Fatalf("discovery did not propagate build error: %v", err)
	}
}

func writeLayoutFixture(t *testing.T, root, path string, data []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
