//go:build mage

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IntegrationAll runs every command's integration suites, allowing static Pending.
// APH_INTEGRATION_REPORT_DIR optionally names a new directory outside the checkout.
func IntegrationAll() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	args := []string{"test", "-tags", "integration", "-run", "^TestIntegration", "-count=1", "-timeout", "5m", "-json", "./cmd/..."}
	return runIntegrationTests(root, os.Getenv("APH_INTEGRATION_REPORT_DIR"), args, os.Stdout, os.Stderr)
}

func runIntegrationTests(root, reportDir string, args []string, stdout, stderr io.Writer) error {
	var err error
	if reportDir == "" {
		reportDir, err = os.MkdirTemp("", "aph-integration-")
	} else {
		if !filepath.IsAbs(reportDir) {
			reportDir = filepath.Join(root, reportDir)
		}
		var relative string
		relative, err = filepath.Rel(root, reportDir)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("integration report directory must be outside the checkout")
		}
		// Different Windows volumes are also outside the checkout.
		if err != nil && filepath.VolumeName(root) == filepath.VolumeName(reportDir) {
			return fmt.Errorf("resolve integration report directory: %w", err)
		}
		if err = os.MkdirAll(filepath.Dir(reportDir), 0o700); err == nil {
			err = os.Mkdir(reportDir, 0o700)
		}
	}
	if err != nil {
		return fmt.Errorf("create fresh integration report directory: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "Product integration reports: %s\n", reportDir); err != nil {
		return err
	}
	results, err := os.Create(filepath.Join(reportDir, "test-results.json"))
	if err != nil {
		return err
	}
	console, err := os.Create(filepath.Join(reportDir, "console.log"))
	if err != nil {
		return errors.Join(err, results.Close())
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Stdout = io.MultiWriter(stdout, results, console)
	cmd.Stderr = io.MultiWriter(stderr, console)
	runErr := cmd.Run()
	if runErr != nil {
		runErr = fmt.Errorf("product integration execution: %w", runErr)
	}
	return errors.Join(runErr, results.Close(), console.Close())
}
