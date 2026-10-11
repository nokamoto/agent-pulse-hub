//go:build windows

package manualplugin

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

//go:generate go run go.uber.org/mock/mockgen -source=filesystem_windows.go -destination=filesystem_windows_mock_test.go -package=manualplugin -build_constraint=windows

type triggerFiles interface {
	LocalDrive(root string) bool
	DirectoryKey(parent, name string) (targetKey, error)
	Exists(path string) error
	Claim(target watchTarget) (string, error)
	Open(path string) (io.ReadCloser, error)
	Remove(path string) error
}

type localTriggerFiles struct{}

func (localTriggerFiles) LocalDrive(root string) bool {
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return false
	}
	driveType := windows.GetDriveType(path)
	return driveType == windows.DRIVE_FIXED || driveType == windows.DRIVE_REMOVABLE || driveType == windows.DRIVE_RAMDISK
}

func (localTriggerFiles) DirectoryKey(parent, name string) (targetKey, error) {
	return directoryKey(parent, name)
}

func (localTriggerFiles) Exists(path string) error {
	_, err := os.Lstat(path)
	return err
}

func (localTriggerFiles) Claim(target watchTarget) (string, error) { return claimFile(target) }
func (localTriggerFiles) Open(path string) (io.ReadCloser, error)  { return os.Open(path) }
func (localTriggerFiles) Remove(path string) error                 { return os.Remove(path) }
