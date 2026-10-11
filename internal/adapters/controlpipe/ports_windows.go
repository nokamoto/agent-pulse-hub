//go:build windows

package controlpipe

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/windowsidentity"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"golang.org/x/sys/windows"
)

//go:generate go run go.uber.org/mock/mockgen -source=ports_windows.go -destination=ports_windows_mock_test.go -package=controlpipe -build_constraint=windows
//go:generate go run go.uber.org/mock/mockgen -destination=io_windows_mock_test.go -package=controlpipe -build_constraint=windows net Conn,Listener

type registrar interface {
	Register(string, string, *jsonvalue.Value) (domain.SubscriptionID, *hub.Error)
}

type pipeSystem interface {
	CurrentUserSID() (string, error)
	ServerUserSID(net.Conn) (string, error)
	Dial(context.Context, string) (net.Conn, error)
	Listen(string, string) (net.Listener, error)
}

type windowsPipes struct{}

func (windowsPipes) CurrentUserSID() (string, error) { return windowsidentity.CurrentUserSID() }
func (windowsPipes) Dial(ctx context.Context, name string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, name)
}

func (windowsPipes) Listen(name, descriptor string) (net.Listener, error) {
	return winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: descriptor})
}

func (windowsPipes) ServerUserSID(connection net.Conn) (string, error) {
	handleProvider, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return "", errors.New("control pipe does not expose its process handle")
	}
	var serverPID uint32
	if err := windows.GetNamedPipeServerProcessId(windows.Handle(handleProvider.Fd()), &serverPID); err != nil {
		return "", fmt.Errorf("verify daemon process identity: %w", err)
	}
	sid, err := windowsidentity.ProcessUserSID(serverPID)
	if err != nil {
		return "", fmt.Errorf("verify daemon user identity: %w", err)
	}
	return sid, nil
}
