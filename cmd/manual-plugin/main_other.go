//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	_, _ = fmt.Fprintln(os.Stderr, "manual-plugin is supported on Windows only")
	os.Exit(1)
}
