//go:build mage

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/onsi/ginkgo/v2/types"
)

type acceptanceInventory struct {
	Suites []acceptanceSuite `json:"suites"`
}

type acceptanceSuite struct {
	Package string   `json:"package"`
	Cases   []string `json:"cases"`
}

var caseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Integration runs the inventory's tagged suites and requires every scoped case to pass.
// Both arguments are required; reportDir must not already exist.
func Integration(inventory, reportDir string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	return runIntegration(root, inventory, reportDir, os.Stdout, os.Stderr)
}

// IntegrationTooling qualifies the runner and report gate, not MVP acceptance.
func IntegrationTooling() error {
	return runGoCommands([][]string{
		{"test", "-tags", "mage,integration", "-run", "^TestIntegrationTooling$", "-count=1", "-timeout", "5m", "-json", "./build"},
	})
}

func decodeInventory(reader io.Reader) (acceptanceInventory, error) {
	var inventory acceptanceInventory
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return inventory, fmt.Errorf("decode acceptance inventory: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return inventory, errors.New("acceptance inventory must contain one JSON object")
	}
	if len(inventory.Suites) == 0 {
		return inventory, errors.New("acceptance inventory must contain at least one suite")
	}
	packages := map[string]bool{}
	ids := map[string]bool{}
	for _, suite := range inventory.Suites {
		path := strings.TrimPrefix(suite.Package, "./")
		if !strings.HasPrefix(suite.Package, "./") || path == "" || strings.Contains(path, "\\") || strings.Contains(path, "...") || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") || filepath.IsAbs(path) {
			return inventory, fmt.Errorf("suite package %q must be an explicit repository-relative directory", suite.Package)
		}
		if packages[suite.Package] || len(suite.Cases) == 0 {
			return inventory, fmt.Errorf("suite %q must be unique and have nonempty cases", suite.Package)
		}
		packages[suite.Package] = true
		for _, id := range suite.Cases {
			if !caseIDPattern.MatchString(id) || ids[id] {
				return inventory, fmt.Errorf("case ID %q must be a unique nonempty identifier", id)
			}
			ids[id] = true
		}
	}
	return inventory, nil
}

func integrationArgs(inventory acceptanceInventory, reportDir string) []string {
	var labels, packages []string
	for _, suite := range inventory.Suites {
		packages = append(packages, suite.Package)
		for _, id := range suite.Cases {
			labels = append(labels, "case:"+id)
		}
	}
	args := []string{"run", "github.com/onsi/ginkgo/v2/ginkgo", "--tags=integration", "--timeout=2m", "--fail-on-empty", "--no-color", "--json-report=report.json", "--output-dir=" + reportDir, "--label-filter=" + strings.Join(labels, " || ")}
	args = append(args, packages...)
	return append(args, "--", "-test.run=^TestIntegration")
}

func runIntegration(root, inventoryPath, reportDir string, stdout, stderr io.Writer) error {
	if !filepath.IsAbs(inventoryPath) {
		inventoryPath = filepath.Join(root, inventoryPath)
	}
	file, err := os.Open(inventoryPath)
	if err != nil {
		return fmt.Errorf("open acceptance inventory: %w", err)
	}
	inventory, err := decodeInventory(file)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !filepath.IsAbs(reportDir) {
		reportDir = filepath.Join(root, reportDir)
	}
	if err := os.MkdirAll(filepath.Dir(reportDir), 0o700); err != nil {
		return err
	}
	// A fresh directory prevents a failed invocation from reusing stale success.
	if err := os.Mkdir(reportDir, 0o700); err != nil {
		return fmt.Errorf("create fresh report directory: %w", err)
	}
	fmt.Fprintf(stdout, "Integration reports: %s\n", reportDir)
	cmd := exec.Command("go", integrationArgs(inventory, reportDir)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, stdout, stderr
	runErr := cmd.Run()
	reportBytes, readErr := os.ReadFile(filepath.Join(reportDir, "report.json"))
	var gateErr error
	if readErr != nil {
		gateErr = fmt.Errorf("read Ginkgo report: %w", readErr)
	} else {
		var reports []types.Report
		if err := json.Unmarshal(reportBytes, &reports); err != nil {
			gateErr = fmt.Errorf("decode Ginkgo report: %w", err)
		} else {
			gateErr = checkAcceptance(root, inventory, reports)
		}
	}
	if runErr != nil {
		runErr = fmt.Errorf("ginkgo execution: %w", runErr)
	}
	if err := errors.Join(runErr, gateErr); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "All inventory cases passed the integration gate")
	return nil
}

func checkAcceptance(root string, inventory acceptanceInventory, reports []types.Report) error {
	if len(inventory.Suites) == 0 || len(reports) == 0 {
		return errors.New("acceptance inventory and execution report must be nonempty")
	}
	for _, suite := range inventory.Suites {
		wantPath := filepath.Join(root, suite.Package)
		var matching []types.Report
		for _, report := range reports {
			if filepath.Clean(report.SuitePath) == filepath.Clean(wantPath) {
				matching = append(matching, report)
			}
		}
		if len(matching) != 1 {
			return fmt.Errorf("suite %s: expected one report, got %d", suite.Package, len(matching))
		}
		report := matching[0]
		if !report.SuiteSucceeded || report.SuiteHasProgrammaticFocus || report.SuiteConfig.DryRun {
			return fmt.Errorf("suite %s: failed, focused, or dry-run report cannot establish acceptance", suite.Package)
		}
		for _, id := range suite.Cases {
			var specs []types.SpecReport
			for _, spec := range report.SpecReports {
				for _, label := range spec.Labels() {
					if label == "case:"+id {
						specs = append(specs, spec)
					}
				}
			}
			if len(specs) != 1 {
				return fmt.Errorf("case %s in %s: expected one spec, got %d", id, suite.Package, len(specs))
			}
			spec := specs[0]
			if spec.LeafNodeType != types.NodeTypeIt || spec.State != types.SpecStatePassed {
				return fmt.Errorf("case %s in %s: expected passed It, got %s (%s)", id, suite.Package, spec.LeafNodeType, spec.State)
			}
			var caseLabels int
			for _, label := range spec.Labels() {
				if strings.HasPrefix(label, "case:") {
					caseLabels++
				}
			}
			if caseLabels != 1 {
				return fmt.Errorf("case %s: one spec cannot represent multiple case IDs", id)
			}
		}
	}
	return nil
}
