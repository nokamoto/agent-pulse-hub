//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/manualplugin"
)

func main() {
	if len(os.Args) != 1 {
		_, _ = fmt.Fprintln(os.Stderr, "manual-plugin does not accept command-line arguments")
		os.Exit(1)
	}
	if err := manualplugin.Run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
