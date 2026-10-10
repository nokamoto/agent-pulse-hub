//go:build windows

package manualplugin

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func isLocalDirectory(path string) bool {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return false
	}
	root, err := windows.UTF16PtrFromString(filepath.Clean(volume + `\`))
	if err != nil {
		return false
	}
	switch windows.GetDriveType(root) {
	case windows.DRIVE_REMOVABLE, windows.DRIVE_FIXED, windows.DRIVE_RAMDISK:
		return true
	default:
		return false
	}
}
