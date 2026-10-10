//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
	})
}

func Lint() error {
	return runGoCommands([][]string{
		{"vet", "./..."},
		{"run", "honnef.co/go/tools/cmd/staticcheck", "./..."},
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
