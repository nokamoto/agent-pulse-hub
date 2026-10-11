//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// These testing.T probes qualify design choices with fixture processes. They
// neither implement the product nor satisfy any Pending MVP acceptance case.
const feasibilitySessionMethod = "/agentpulse.plugin.v1.EventPlugin/Session"

var feasibilityHandshake = plugin.HandshakeConfig{
	ProtocolVersion: 1, MagicCookieKey: "AGENT_PULSE_HUB_PLUGIN", MagicCookieValue: "plugin-v1",
}

type feasibilityFrame struct {
	Version        int             `json:"version"`
	Type           string          `json:"type"`
	RequestID      string          `json:"request_id,omitempty"`
	SubscriptionID string          `json:"subscription_id,omitempty"`
	WatchArgs      json.RawMessage `json:"watch_args,omitempty"`
	Accepted       *bool           `json:"accepted,omitempty"`
	Error          string          `json:"error,omitempty"`
	Context        string          `json:"context,omitempty"`
}

type feasibilitySessionServer interface {
	Session(grpc.ServerStream) error
}

type feasibilityEventPlugin struct{ plugin.NetRPCUnsupportedPlugin }

func (p feasibilityEventPlugin) GRPCServer(_ *plugin.GRPCBroker, server *grpc.Server) error {
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "agentpulse.plugin.v1.EventPlugin",
		HandlerType: (*feasibilitySessionServer)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName: "Session", ServerStreams: true, ClientStreams: true,
			Handler: func(service any, stream grpc.ServerStream) error {
				return service.(feasibilitySessionServer).Session(stream)
			},
		}},
	}, p)
	return nil
}

func (feasibilityEventPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return conn, nil
}

func (feasibilityEventPlugin) Session(stream grpc.ServerStream) error {
	if err := feasibilitySend(stream, feasibilityFrame{Version: 1, Type: "ready"}); err != nil {
		return err
	}
	for {
		var message wrapperspb.BytesValue
		if err := stream.RecvMsg(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		watch, err := feasibilityDecode(message.Value)
		if err != nil {
			return err
		}
		if watch.Version != 1 || watch.Type != "watch" || watch.RequestID == "" || watch.SubscriptionID == "" {
			return errors.New("fixture expected a watch")
		}
		var args struct {
			Context string `json:"context"`
			Reject  bool   `json:"reject"`
		}
		if err := json.Unmarshal(watch.WatchArgs, &args); err != nil {
			return err
		}
		accepted := !args.Reject
		result := feasibilityFrame{Version: 1, Type: "watch_result", RequestID: watch.RequestID, Accepted: &accepted}
		if !accepted {
			result.Error = "fixture rejection"
		}
		if err := feasibilitySend(stream, result); err != nil {
			return err
		}
		if accepted {
			// Immediate emission deliberately tests ordering on this same stream.
			if err := feasibilitySend(stream, feasibilityFrame{Version: 1, Type: "event", SubscriptionID: watch.SubscriptionID, Context: args.Context}); err != nil {
				return err
			}
		}
	}
}

func feasibilitySend(stream interface{ SendMsg(any) error }, frame feasibilityFrame) error {
	value, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return stream.SendMsg(wrapperspb.Bytes(value))
}

func feasibilityDecode(value []byte) (feasibilityFrame, error) {
	// This fixture decoder handles the valid transcript used by the probe. The
	// product's duplicate-key, surrogate, schema and limit matrices remain unit
	// verification work; this is not a replacement protocol implementation.
	var frame feasibilityFrame
	if !utf8.Valid(value) {
		return frame, errors.New("fixture received invalid UTF-8")
	}
	err := json.Unmarshal(value, &frame)
	return frame, err
}

// Only the separately launched test binary enters plugin.Serve.
func TestPluginSessionFeasibilityHelperProcess(t *testing.T) {
	if os.Getenv("APH_SESSION_FEASIBILITY_HELPER") != "1" {
		return
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: feasibilityHandshake,
		Plugins:         plugin.PluginSet{"events": feasibilityEventPlugin{}},
		GRPCServer:      plugin.DefaultGRPCServer, Logger: hclog.NewNullLogger(),
	})
}

func TestPluginSessionFeasibility(t *testing.T) {
	for _, language := range []string{"Go", "Python"} {
		t.Run(language, func(t *testing.T) {
			started := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var cmd *exec.Cmd
			if language == "Go" {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd = exec.CommandContext(ctx, executable, "-test.run=^TestPluginSessionFeasibilityHelperProcess$")
				cmd.Env = append(os.Environ(), "APH_SESSION_FEASIBILITY_HELPER=1")
			} else {
				python := os.Getenv("APH_PLUGIN_PYTHON")
				if python == "" {
					python = "python"
				}
				cmd = exec.CommandContext(ctx, python, "-B", filepath.Join("testdata", "plugin-session.py"))
			}
			client := plugin.NewClient(&plugin.ClientConfig{
				HandshakeConfig: feasibilityHandshake,
				Plugins:         plugin.PluginSet{"events": feasibilityEventPlugin{}}, Cmd: cmd,
				AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: 5 * time.Second,
				Logger: hclog.NewNullLogger(), Stderr: os.Stderr,
			})
			defer feasibilityKill(t, client, cancel)
			rpcClient, err := client.Client()
			if err != nil {
				t.Fatalf("start %s plugin: %v", language, err)
			}
			if client.Protocol() != plugin.ProtocolGRPC {
				t.Fatalf("negotiated protocol %q, want gRPC", client.Protocol())
			}
			raw, err := rpcClient.Dispense("events")
			if err != nil {
				t.Fatal(err)
			}
			conn := raw.(*grpc.ClientConn)
			health, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: plugin.GRPCServiceName})
			if err != nil || health.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
				t.Fatalf("standard go-plugin health: %v, %v", health, err)
			}
			stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, feasibilitySessionMethod)
			if err != nil {
				t.Fatal(err)
			}
			receive := func(expected feasibilityFrame) {
				t.Helper()
				var message wrapperspb.BytesValue
				if err := stream.RecvMsg(&message); err != nil {
					t.Fatal(err)
				}
				actual, err := feasibilityDecode(message.Value)
				if err != nil || !reflect.DeepEqual(actual, expected) {
					t.Fatalf("Session message: got %+v, want %+v, error=%v", actual, expected, err)
				}
				t.Logf("%s Session received: %s", language, message.Value)
			}
			receive(feasibilityFrame{Version: 1, Type: "ready"})
			accepted, rejected := true, false
			contextValue := "Immediate event: 日本語\nquoted \"value\" C:\\probe"
			args, err := json.Marshal(map[string]any{"context": contextValue})
			if err != nil {
				t.Fatal(err)
			}
			watch := feasibilityFrame{Version: 1, Type: "watch", RequestID: "opaque request 日本語", SubscriptionID: "opaque subscription α", WatchArgs: args}
			if err := feasibilitySend(stream, watch); err != nil {
				t.Fatal(err)
			}
			receive(feasibilityFrame{Version: 1, Type: "watch_result", RequestID: watch.RequestID, Accepted: &accepted})
			receive(feasibilityFrame{Version: 1, Type: "event", SubscriptionID: watch.SubscriptionID, Context: contextValue})
			watch.RequestID, watch.SubscriptionID, watch.WatchArgs = "rejected request", "rejected subscription", json.RawMessage(`{"reject":true}`)
			if err := feasibilitySend(stream, watch); err != nil {
				t.Fatal(err)
			}
			receive(feasibilityFrame{Version: 1, Type: "watch_result", RequestID: watch.RequestID, Accepted: &rejected, Error: "fixture rejection"})
			if err := stream.CloseSend(); err != nil {
				t.Fatal(err)
			}
			var trailing wrapperspb.BytesValue
			if err := stream.RecvMsg(&trailing); !errors.Is(err, io.EOF) {
				t.Fatalf("Session termination: got %v, want EOF without rejection event", err)
			}
			cleanupStarted := time.Now()
			feasibilityKill(t, client, cancel)
			if !client.Exited() || cmd.ProcessState == nil || !cmd.ProcessState.Success() {
				t.Fatalf("controller shutdown was not a normal process exit: exited=%v, state=%v", client.Exited(), cmd.ProcessState)
			}
			t.Logf("%s: one direct child PID=%d, one bidirectional Session; opaque IDs and UTF-8 context roundtrip; accepted result before immediate event; rejected watch with no event; controller shutdown and direct process exit confirmed; elapsed=%s cleanup=%s", language, cmd.Process.Pid, time.Since(started), time.Since(cleanupStarted))
		})
	}
}

func feasibilityKill(t *testing.T, client *plugin.Client, cancel context.CancelFunc) {
	t.Helper()
	done := make(chan struct{})
	go func() { client.Kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("fixture process cleanup did not finish after cancellation")
		}
		t.Error("fixture cleanup exceeded five seconds")
	}
}

func TestLoopbackControlFeasibility(t *testing.T) {
	started := time.Now()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if address := listener.Addr().(*net.TCPAddr); !address.IP.IsLoopback() {
		t.Fatalf("listener is not loopback: %v", address)
	}
	conflict, err := net.Listen("tcp4", listener.Addr().String())
	if err == nil {
		conflict.Close()
		t.Fatal("second listener unexpectedly acquired the same endpoint")
	}
	t.Logf("second listener failed visibly: %v", err)
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			done <- err
			return
		}
		var request map[string]any
		if err := json.NewDecoder(connection).Decode(&request); err != nil {
			done <- err
			return
		}
		done <- json.NewEncoder(connection).Encode(request)
	}()
	connection, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{"version": float64(1), "op": "register", "plugin": "probe", "session_id": "opaque id", "watch_args": map[string]any{"context": "日本語\nquoted \"value\""}}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, response) {
		t.Fatalf("JSON roundtrip: got %#v, want %#v", response, request)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loopback fixture did not finish")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	// Rebinding proves that cleanup released the listener, rather than merely
	// returning before the fixture goroutine finished.
	rebound, err := net.Listen("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("loopback JSON roundtrip, occupied-port failure and listener release confirmed; zero children, elapsed=%s", time.Since(started))
}
