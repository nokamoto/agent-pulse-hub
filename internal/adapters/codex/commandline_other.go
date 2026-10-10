//go:build !windows

package codex

import (
	"strings"
	"unicode/utf16"
)

func commandLineUnits(executable string, arguments []string) int {
	parts := append([]string{executable}, arguments...)
	return len(utf16.Encode([]rune(strings.Join(parts, " "))))
}
