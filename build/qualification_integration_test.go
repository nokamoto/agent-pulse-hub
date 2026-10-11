//go:build mage && integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
)

// These tooling tests exercise real Ginkgo reports; they are not product cases.
func TestIntegrationTooling(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	outputDir := os.Getenv("APH_TOOLING_REPORT_DIR")
	if outputDir == "" {
		outputDir = t.TempDir()
	} else if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		ids   []string
		valid bool
		state types.SpecState
	}{
		{"passed", []string{"TOOL-PASS"}, true, types.SpecStatePassed},
		{"pending", []string{"TOOL-PENDING"}, false, types.SpecStatePending},
		{"skip", []string{"TOOL-SKIP"}, false, types.SpecStateSkipped},
		{"missing", []string{"TOOL-PASS", "TOOL-MISSING"}, false, types.SpecStatePassed},
		{"failed", []string{"TOOL-FAIL"}, false, types.SpecStateFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inventory := acceptanceInventory{Suites: []acceptanceSuite{{Package: "./build/testdata/qualification", Cases: tt.ids}}}
			data, err := json.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			inventoryPath := filepath.Join(t.TempDir(), "cases.json")
			if err := os.WriteFile(inventoryPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			reportDir := filepath.Join(outputDir, tt.name)
			var output bytes.Buffer
			err = runIntegration(root, inventoryPath, reportDir, &output, &output)
			t.Log(output.String())
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
			if !tt.valid {
				t.Logf("Gate correctly rejected %s: %v", tt.name, err)
			}
			reportBytes, err := os.ReadFile(filepath.Join(reportDir, "report.json"))
			if err != nil {
				t.Fatalf("read real Ginkgo report: %v", err)
			}
			var reports []types.Report
			if err := json.Unmarshal(reportBytes, &reports); err != nil {
				t.Fatal(err)
			}
			var observed bool
			for _, report := range reports {
				for _, spec := range report.SpecReports {
					for _, label := range spec.Labels() {
						if label == "case:"+tt.ids[0] && spec.State == tt.state {
							observed = true
						}
					}
				}
			}
			if !observed {
				t.Fatalf("fixture did not produce the required %s state for %s", tt.state, tt.ids[0])
			}
			if tt.valid {
				if err := runIntegration(root, inventoryPath, reportDir, &output, &output); err == nil || !strings.Contains(err.Error(), "fresh report directory") {
					t.Fatalf("stale report directory was not rejected: %v", err)
				}
			}
		})
	}
	t.Run("automatic runner", func(t *testing.T) {
		qualifyIntegrationAll(t, root, filepath.Join(outputDir, "automatic-runner"))
	})
}
