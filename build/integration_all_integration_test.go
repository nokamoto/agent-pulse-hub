//go:build mage && integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Qualification uses real fixtures to check exit status and retained evidence.
// Fixture selection applies only here; IntegrationAll has no case filter.
func qualifyIntegrationAll(t *testing.T, root, outputDir string) {
	t.Helper()
	for _, tt := range []struct {
		name, label, action string
		valid               bool
	}{
		{"passed", "TOOL-PASS", "pass", true},
		{"pending", "TOOL-PENDING", "pass", true},
		{"failed", "TOOL-FAIL", "fail", false},
		{"build error", "", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"test", "-tags", "integration", "-run", "^TestIntegration", "-count=1", "-timeout", "5m", "-json"}
			if tt.label == "" {
				args = append(args, "./build/testdata/missing-package")
			} else {
				args = append(args, "./build/testdata/qualification", "-args", "-ginkgo.no-color", "-ginkgo.label-filter=case:"+tt.label)
			}
			reportDir := filepath.Join(outputDir, tt.name)
			var stdout, stderr bytes.Buffer
			err := runIntegrationTests(root, reportDir, args, &stdout, &stderr)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v\n%s\n%s", tt.valid, err, stdout.String(), stderr.String())
			}
			console, err := os.ReadFile(filepath.Join(reportDir, "console.log"))
			if err != nil || len(console) == 0 {
				t.Fatalf("missing execution log: %v", err)
			}
			results, err := os.ReadFile(filepath.Join(reportDir, "test-results.json"))
			if err != nil {
				t.Fatal(err)
			}
			if tt.action != "" {
				decoder := json.NewDecoder(bytes.NewReader(results))
				var observed bool
				for {
					var event struct {
						Action, Test string
					}
					if err := decoder.Decode(&event); err == io.EOF {
						break
					} else if err != nil {
						t.Fatalf("invalid Go test JSON: %v", err)
					}
					if event.Test == "TestIntegrationQualification" && event.Action == tt.action {
						observed = true
					}
				}
				if !observed {
					t.Fatalf("missing test %s event in report", tt.action)
				}
			}
			if tt.name == "pending" && !bytes.Contains(results, []byte("1 Pending")) {
				t.Fatal("fixture did not retain its static Pending result")
			}
			if tt.valid {
				if err := runIntegrationTests(root, reportDir, args, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "fresh integration report directory") {
					t.Fatalf("stale report directory was not rejected: %v", err)
				}
			}
		})
	}
	if err := runIntegrationTests(root, filepath.Join(root, "integration-report"), nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "outside the checkout") {
		t.Fatalf("report directory inside checkout was not rejected: %v", err)
	}
}
