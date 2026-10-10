//go:build !windows

package manualplugin

func isLocalDirectory(string) bool {
	return true
}
