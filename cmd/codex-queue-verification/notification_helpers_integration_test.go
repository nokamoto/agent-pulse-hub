//go:build integration

package main

import (
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"google.golang.org/grpc"
)

func startNotificationAction(t acceptanceT, actionPath, codexPath string, environment []string) *publicActionSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	command := exec.CommandContext(ctx, actionPath, "--codex-executable", codexPath, "--notification-delivery")
	command.Env = environment
	command.WaitDelay = time.Second
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  pluginprocess.HandshakeConfig,
		Plugins:          plugin.PluginSet{"output": publicOutput{}},
		Cmd:              command,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout:     10 * time.Second,
		Logger:           hclog.NewNullLogger(),
		Stderr:           io.Discard,
	})
	cleanup := &publicActionSession{client: client}
	t.Cleanup(func() {
		cancel()
		cleanup.close(t)
	})
	rpc, err := client.Client()
	if err != nil {
		t.Fatalf("start actual action through go-plugin: %v", err)
	}
	value, err := rpc.Dispense("output")
	if err != nil {
		t.Fatalf("dispense output role: %v", err)
	}
	connection, ok := value.(*grpc.ClientConn)
	if !ok {
		t.Fatalf("dispensed output connection has type %T", value)
	}
	stream, err := pluginprotocol.OpenSession(ctx, connection)
	if err != nil {
		t.Fatalf("open public v2 Session: %v", err)
	}
	cleanup.stream = stream
	ready := receiveActionFrame(t, stream)
	if ready.Type != "ready" || ready.Role != "output" || len(ready.Capabilities) != 1 || ready.Capabilities[0] != pluginprotocol.NotificationCapability {
		t.Fatalf("readiness frame=%+v", ready)
	}
	return cleanup
}
