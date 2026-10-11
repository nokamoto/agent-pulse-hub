//go:build mage && integration

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// This uses the standard gRPC health API, not a product plugin contract.
type qualificationHealthPlugin struct {
	plugin.NetRPCUnsupportedPlugin
}

func (qualificationHealthPlugin) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	// go-plugin's server already registers the standard health service.
	return nil
}

func (qualificationHealthPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return grpc_health_v1.NewHealthClient(conn), nil
}

var qualificationHandshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "APH_TOOLING_PLUGIN",
	MagicCookieValue: "qualification",
}

func qualificationPluginSet() plugin.PluginSet {
	return plugin.PluginSet{"health": qualificationHealthPlugin{}}
}

// This entry point is executed only in a separately spawned test binary.
func TestPluginToolingHelperProcess(t *testing.T) {
	mode := os.Getenv("APH_TOOLING_HELPER_MODE")
	if mode == "" {
		return
	}
	handshake := qualificationHandshake
	if mode == "wrong-version" {
		handshake.ProtocolVersion = 2
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: handshake,
		Plugins:         qualificationPluginSet(),
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          hclog.NewNullLogger(),
	})
}

func TestPluginTooling(t *testing.T) {
	python := os.Getenv("APH_PLUGIN_PYTHON")
	if python == "" {
		python = "python"
	}
	for _, language := range []string{"Go", "Python"} {
		t.Run(language, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var cmd *exec.Cmd
			if language == "Go" {
				cmd = qualificationGoCommand(t, ctx, "healthy")
			} else {
				cmd = exec.CommandContext(ctx, python, "-B", filepath.Join("testdata", "plugin-tooling", "plugin.py"))
			}
			client := qualificationClient(cmd)
			defer qualificationKill(t, client, cancel)
			rpcClient, err := client.Client()
			if err != nil {
				t.Fatalf("start %s plugin: %v", language, err)
			}
			if client.Protocol() != plugin.ProtocolGRPC {
				t.Fatalf("negotiated protocol %q, want gRPC", client.Protocol())
			}
			raw, err := rpcClient.Dispense("health")
			if err != nil {
				t.Fatalf("dispense health client: %v", err)
			}
			health := raw.(grpc_health_v1.HealthClient)
			rpcCtx, rpcCancel := context.WithTimeout(ctx, 5*time.Second)
			defer rpcCancel()
			request := &grpc_health_v1.HealthCheckRequest{Service: plugin.GRPCServiceName}
			response, err := health.Check(rpcCtx, request)
			if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
				t.Fatalf("unary health Check: response=%v, error=%v", response, err)
			}
			stream, err := health.Watch(rpcCtx, request)
			if err != nil {
				t.Fatalf("open health Watch stream: %v", err)
			}
			response, err = stream.Recv()
			if err != nil || response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
				t.Fatalf("stream health Watch: response=%v, error=%v", response, err)
			}
			rpcCancel()
			qualificationKill(t, client, cancel)
			if !client.Exited() || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
				t.Fatalf("plugin did not exit normally through Client.Kill: exited=%v, state=%v", client.Exited(), cmd.ProcessState)
			}
			t.Logf("%s: gRPC negotiation, unary RPC, server stream, and normal subprocess shutdown passed", language)
		})
	}
	for _, failure := range []string{"wrong-version", "missing-executable"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := qualificationGoCommand(t, ctx, "wrong-version")
			if failure == "missing-executable" {
				cmd = exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-plugin"))
			}
			client := qualificationClient(cmd)
			defer qualificationKill(t, client, cancel)
			if _, err := client.Client(); err == nil {
				t.Fatalf("%s plugin unexpectedly connected", failure)
			} else {
				t.Logf("%s correctly failed: %v", failure, err)
			}
		})
	}
}

func qualificationGoCommand(t *testing.T, ctx context.Context, mode string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPluginToolingHelperProcess$")
	cmd.Env = append(os.Environ(), "APH_TOOLING_HELPER_MODE="+mode)
	return cmd
}

func qualificationClient(cmd *exec.Cmd) *plugin.Client {
	return plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  qualificationHandshake,
		Plugins:          qualificationPluginSet(),
		Cmd:              cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout:     5 * time.Second,
		Logger:           hclog.NewNullLogger(),
		Stderr:           os.Stderr,
	})
}

func qualificationKill(t *testing.T, client *plugin.Client, cancel context.CancelFunc) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		client.Kill()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("plugin cleanup did not finish after process cancellation")
		}
		t.Error("plugin cleanup exceeded five seconds")
	}
}
