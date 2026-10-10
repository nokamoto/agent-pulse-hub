//go:build windows

package codex

import (
	"strings"
	"syscall"
	"unicode/utf16"
)

func commandLineUnits(executable string, arguments []string) int {
	parts := make([]string, 0, len(arguments)+1)
	parts = append(parts, syscall.EscapeArg(executable))
	for _, argument := range arguments {
		parts = append(parts, syscall.EscapeArg(argument))
	}
	return len(utf16.Encode([]rune(strings.Join(parts, " "))))
}
