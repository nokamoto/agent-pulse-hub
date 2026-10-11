//go:build windows

package manualplugin

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var compareStringOrdinal = syscall.NewLazyDLL("kernel32.dll").NewProc("CompareStringOrdinal")

type targetKey struct {
	volume uint32
	index  uint64
	name   string
}

type watchTarget struct {
	path      string
	directory string
	key       targetKey
}

func parseWatchTargetWithFiles(input string, files triggerFiles) (watchTarget, error) {
	path, parent, name, err := normalizeTriggerPath(input)
	if err != nil {
		return watchTarget{}, err
	}
	if !files.LocalDrive(path[:3]) {
		return watchTarget{}, errors.New("trigger path must be on a local drive")
	}
	key, err := files.DirectoryKey(parent, name)
	if err != nil {
		return watchTarget{}, err
	}
	if err := files.Exists(path); err == nil {
		return watchTarget{}, errors.New("trigger file must not already exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return watchTarget{}, errors.New("trigger file path is unavailable")
	}
	return watchTarget{path: path, directory: parent, key: key}, nil
}

func normalizeTriggerPath(input string) (path, parent, name string, err error) {
	path = strings.ReplaceAll(input, "/", `\`)
	if len(path) < 4 || !isDriveLetter(path[0]) || path[1] != ':' || path[2] != '\\' || strings.HasSuffix(path, `\`) {
		return "", "", "", errors.New("trigger_file must be an absolute drive path to a file")
	}
	if strings.Contains(path[2:], ":") {
		return "", "", "", errors.New("alternate data streams are not supported")
	}
	drive := strings.ToUpper(path[:1]) + `:\`
	parts := strings.Split(path[3:], `\`)
	stack := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) == 0 {
				return "", "", "", errors.New("trigger path escapes its drive root")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return "", "", "", errors.New("path components ending in a dot or space are unsupported")
		}
		stack = append(stack, part)
	}
	if len(stack) == 0 {
		return "", "", "", errors.New("trigger_file must name a file")
	}
	name = stack[len(stack)-1]
	parent = drive + strings.Join(stack[:len(stack)-1], `\`)
	if len(stack) > 1 {
		parent = drive + strings.Join(stack[:len(stack)-1], `\`)
	}
	path = drive + strings.Join(stack, `\`)
	return path, parent, name, nil
}

func directoryKey(parent, name string) (targetKey, error) {
	path, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		return targetKey{}, err
	}
	handle, err := windows.CreateFile(path, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return targetKey{}, errors.New("trigger parent directory identity is unavailable")
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return targetKey{}, errors.New("trigger parent is not an accessible directory")
	}
	return targetKey{
		volume: info.VolumeSerialNumber,
		index:  uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		name:   name,
	}, nil
}

func sameTarget(first, second targetKey) bool {
	if first.volume != second.volume || first.index != second.index {
		return false
	}
	firstName, err := windows.UTF16FromString(first.name)
	if err != nil {
		return false
	}
	secondName, err := windows.UTF16FromString(second.name)
	if err != nil {
		return false
	}
	result, _, _ := compareStringOrdinal.Call(
		uintptr(unsafe.Pointer(&firstName[0])), uintptr(len(firstName)-1),
		uintptr(unsafe.Pointer(&secondName[0])), uintptr(len(secondName)-1), 1,
	)
	return result == 2 // CSTR_EQUAL
}

func isDriveLetter(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}

func (target watchTarget) String() string { return target.path }
