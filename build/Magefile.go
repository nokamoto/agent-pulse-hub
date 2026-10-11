//go:build mage

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var Default = Check

// Check runs unit and product integration tests, preserving both results on failure.
func Check() error {
	unitErr := Test()
	integrationErr := IntegrationAll()
	return errors.Join(unitErr, integrationErr)
}

func Guardrails() error {
	return runGoCommands([][]string{
		{"run", "./tools/guardrails/requirements"},
		{"run", "./tools/guardrails/design"},
		{"run", "./tools/guardrails/docs-layout"},
		{"run", "./tools/guardrails/go-layout"},
	})
}

func Generate() error {
	return runGoCommands([][]string{
		{"generate", "./..."},
	})
}

func Format() error {
	return runGoCommands([][]string{
		{"mod", "tidy"},
		{"run", "golang.org/x/tools/cmd/goimports", "-w", "."},
		{"run", "mvdan.cc/gofumpt", "-w", "."},
	})
}

func Test() error {
	return runGoCommands([][]string{
		{"test", "./..."},
		{"test", "-tags", "mage", "./build"},
	})
}

func Lint() error {
	return runGoCommands([][]string{
		{"vet", "./..."},
		{"run", "honnef.co/go/tools/cmd/staticcheck", "./..."},
		{"vet", "-tags", "mage,integration", "./build", "./build/testdata/qualification"},
		{"run", "honnef.co/go/tools/cmd/staticcheck", "-tags", "mage,integration", "./build", "./build/testdata/qualification"},
	})
}

func runGoCommands(commands [][]string) error {
	for _, args := range commands {
		cmd := exec.Command("go", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
		}
	}

	return nil
}
