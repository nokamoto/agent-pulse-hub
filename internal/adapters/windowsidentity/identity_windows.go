//go:build windows

package windowsidentity

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func CurrentUserSID() (string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current user identity: %w", err)
	}
	return user.User.Sid.String(), nil
}

func ProcessUserSID(processID uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, processID)
	if err != nil {
		return "", fmt.Errorf("open named-pipe server process: %w", err)
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token)
	if err != nil {
		return "", fmt.Errorf("read named-pipe server token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read named-pipe server identity: %w", err)
	}
	return user.User.Sid.String(), nil
}
