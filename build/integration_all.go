//go:build mage

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IntegrationAll runs command and bundled plugin integration suites, allowing static Pending.
// APH_INTEGRATION_REPORT_DIR optionally names a new directory outside the checkout.
func IntegrationAll() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	return runIntegrationAll(root, os.Getenv("APH_INTEGRATION_REPORT_DIR"), os.Stdout, os.Stderr)
}

func runIntegrationAll(root, reportDir string, stdout, stderr io.Writer) error {
	// A nil argument list requests discovery after fresh evidence files exist.
	return runIntegrationTests(root, reportDir, nil, stdout, stderr)
}

// Listing the root module avoids wildcard warnings for role directories that
// do not exist yet. The integration tag includes packages containing only suites.
func discoverIntegrationPackages(root string) ([]string, error) {
	cmd := exec.Command("go", "list", "-tags", "integration", "-json", "./...")
	cmd.Dir = root
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("discover integration packages: %w: %s", err, stderr.String())
	}
	decoder := json.NewDecoder(&output)
	var packages []string
	for {
		var pkg struct{ Dir string }
		if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode integration packages: %w", err)
		}
		rel, err := filepath.Rel(root, pkg.Dir)
		if err != nil {
			return nil, err
		}
		path := filepath.ToSlash(rel)
		parts := strings.Split(path, "/")
		if strings.HasPrefix(path, "cmd/") || (len(parts) == 3 && parts[0] == "plugins" && (parts[1] == "triggers" || parts[1] == "actions")) {
			packages = append(packages, "./"+path)
		}
	}
	if len(packages) == 0 {
		return nil, errors.New("no command or bundled plugin integration packages discovered")
	}
	return packages, nil
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
	if args == nil {
		packages, discoveryErr := discoverIntegrationPackages(root)
		if discoveryErr != nil {
			_, logErr := fmt.Fprintln(io.MultiWriter(stderr, console), discoveryErr)
			return errors.Join(discoveryErr, logErr, results.Close(), console.Close())
		}
		args = []string{"test", "-tags", "integration", "-run", "^TestIntegration", "-count=1", "-timeout", "5m", "-json"}
		args = append(args, packages...)
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
