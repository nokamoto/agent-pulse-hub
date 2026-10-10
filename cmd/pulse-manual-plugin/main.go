package main

import (
	"fmt"
	"os"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/manualplugin"
)

func main() {
	server := manualplugin.NewServer(os.Stdin, os.Stdout, os.Stderr)
	if err := server.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
