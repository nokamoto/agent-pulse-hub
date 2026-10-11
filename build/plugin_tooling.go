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

// PluginTooling qualifies go-plugin prerequisites without product API or acceptance cases.
// APH_PLUGIN_TOOLING_REPORT_DIR optionally names a new directory outside the checkout.
func PluginTooling() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	reportDir := os.Getenv("APH_PLUGIN_TOOLING_REPORT_DIR")
	if reportDir == "" {
		reportDir, err = os.MkdirTemp("", "aph-plugin-tooling-")
	} else {
		if !filepath.IsAbs(reportDir) {
			reportDir = filepath.Join(root, reportDir)
		}
		relative, relativeErr := filepath.Rel(root, reportDir)
		if relativeErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("plugin tooling report directory must be outside the checkout")
		}
		if relativeErr != nil && filepath.VolumeName(root) == filepath.VolumeName(reportDir) {
			return fmt.Errorf("resolve plugin tooling report directory: %w", relativeErr)
		}
		if err = os.MkdirAll(filepath.Dir(reportDir), 0o700); err == nil {
			err = os.Mkdir(reportDir, 0o700)
		}
	}
	if err != nil {
		return fmt.Errorf("create fresh plugin tooling report directory: %w", err)
	}
	fmt.Printf("Plugin tooling reports: %s\n", reportDir)
	results, err := os.Create(filepath.Join(reportDir, "test-results.json"))
	if err != nil {
		return err
	}
	console, err := os.Create(filepath.Join(reportDir, "console.log"))
	if err != nil {
		return errors.Join(err, results.Close())
	}
	cmd := exec.Command("go", "test", "-tags", "mage,integration", "-run", "^TestPluginTooling$", "-count=1", "-timeout", "2m", "-json", "./build")
	cmd.Stdout = io.MultiWriter(os.Stdout, results, console)
	cmd.Stderr = io.MultiWriter(os.Stderr, console)
	runErr := cmd.Run()
	if runErr != nil {
		runErr = fmt.Errorf("plugin tooling qualification: %w", runErr)
	}
	return errors.Join(runErr, results.Close(), console.Close())
}
