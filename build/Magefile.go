//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func Guardrails() error {
	commands := [][]string{
		{"run", "./tools/guardrails/requirements"},
	}

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
