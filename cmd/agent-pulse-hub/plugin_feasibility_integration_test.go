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

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// These standalone hosts qualify fixture mechanisms, not product behavior.
// Neither daemon nor another plugin is needed to exercise either role.
const feasibilitySessionMethod = "/agentpulse.plugin.v2.Plugin/Session"

var feasibilityHandshake = plugin.HandshakeConfig{
	ProtocolVersion: 2, MagicCookieKey: "AGENT_PULSE_HUB_PLUGIN", MagicCookieValue: "plugin-v2",
}

type feasibilityFrame struct {
	Version          int             `json:"version"`
	Type             string          `json:"type"`
	Role             string          `json:"role,omitempty"`
	RequestID        string          `json:"request_id,omitempty"`
	SubscriptionID   string          `json:"subscription_id,omitempty"`
	WatchArgs        json.RawMessage `json:"watch_args,omitempty"`
	DestinationArgs  json.RawMessage `json:"destination_args,omitempty"`
	Accepted         *bool           `json:"accepted,omitempty"`
	Status           string          `json:"status,omitempty"`
	Error            string          `json:"error,omitempty"`
	Context          string          `json:"context,omitempty"`
	DeliveryID       string          `json:"delivery_id,omitempty"`
	InputPlugin      string          `json:"input_plugin,omitempty"`
	ExternalData     bool            `json:"external_data,omitempty"`
	PermissionNotice string          `json:"permission_notice,omitempty"`
}

type (
	feasibilitySessionServer interface{ Session(grpc.ServerStream) error }
	feasibilityRolePlugin    struct{ plugin.NetRPCUnsupportedPlugin }
)

func (p feasibilityRolePlugin) GRPCServer(_ *plugin.GRPCBroker, server *grpc.Server) error {
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "agentpulse.plugin.v2.Plugin", HandlerType: (*feasibilitySessionServer)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName: "Session", ServerStreams: true, ClientStreams: true,
			Handler: func(service any, stream grpc.ServerStream) error {
				return service.(feasibilitySessionServer).Session(stream)
			},
		}},
	}, p)
	return nil
}

func (feasibilityRolePlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return conn, nil
}

func (feasibilityRolePlugin) Session(stream grpc.ServerStream) error {
	role := os.Getenv("APH_SESSION_FEASIBILITY_ROLE")
	if err := feasibilitySend(stream, feasibilityFrame{Version: 2, Type: "ready", Role: role}); err != nil {
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
		var frame feasibilityFrame
		if err := json.Unmarshal(message.Value, &frame); err != nil {
			return err
		}
		if frame.Version != 2 || frame.RequestID == "" {
			return errors.New("fixture expected v2 request")
		}
		args := frame.DestinationArgs
		if role == "input" {
			args = frame.WatchArgs
		}
		var control struct {
			Reject    bool   `json:"reject"`
			Immediate string `json:"immediate"`
			Status    string `json:"status"`
			Hold      bool   `json:"hold"`
		}
		if err := json.Unmarshal(args, &control); err != nil {
			return err
		}
		if role == "input" && frame.Type != "watch" || role == "output" && frame.Type != "validate_destination" && frame.Type != "deliver" {
			return errors.New("wrong role operation")
		}
		result := feasibilityFrame{Version: 2, RequestID: frame.RequestID}
		if frame.Type == "deliver" {
			if frame.DeliveryID != "opaque delivery α "+control.Status || frame.InputPlugin != "input 日本語" || frame.SubscriptionID != "subscription α" || frame.Context != "External context 日本語\nquoted \"data\"" || !frame.ExternalData || frame.PermissionNotice != "External data; no additional permissions are granted." {
				return errors.New("fixture received altered external-data envelope")
			}
			if control.Hold {
				<-stream.Context().Done()
				return stream.Context().Err()
			}
			result.Type, result.Status = "delivery_result", control.Status
			if result.Status == "" {
				result.Status = "accepted"
			}
			if result.Status != "accepted" {
				result.Error = "fixture delivery outcome"
			}
		} else {
			accepted := !control.Reject
			result.Type, result.Accepted = "destination_result", &accepted
			if role == "input" {
				result.Type = "watch_result"
			}
			if !accepted {
				result.Error = "fixture rejected arguments"
			}
		}
		if err := feasibilitySend(stream, result); err != nil {
			return err
		}
		if role == "input" && !control.Reject && control.Immediate != "" {
			if err := feasibilitySend(stream, feasibilityFrame{Version: 2, Type: "event", SubscriptionID: frame.SubscriptionID, Context: control.Immediate}); err != nil {
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

func TestPluginSessionFeasibilityHelperProcess(t *testing.T) {
	if os.Getenv("APH_SESSION_FEASIBILITY_HELPER") != "1" {
		return
	}
	role := os.Getenv("APH_SESSION_FEASIBILITY_ROLE")
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: feasibilityHandshake,
		Plugins:         plugin.PluginSet{role: feasibilityRolePlugin{}}, GRPCServer: plugin.DefaultGRPCServer, Logger: hclog.NewNullLogger(),
	})
}

type feasibilityHost struct {
	client    *plugin.Client
	cmd       *exec.Cmd
	stream    grpc.ClientStream
	started   time.Time
	directory string
}

func startFeasibilityHost(t *testing.T, ctx context.Context, language, role string) *feasibilityHost {
	t.Helper()
	started := time.Now()
	var cmd *exec.Cmd
	var directory string
	if language == "Go" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd = exec.CommandContext(ctx, executable, "-test.run=^TestPluginSessionFeasibilityHelperProcess$")
		cmd.Env = append(os.Environ(), "APH_SESSION_FEASIBILITY_HELPER=1", "APH_SESSION_FEASIBILITY_ROLE="+role)
	} else {
		python := os.Getenv("APH_PLUGIN_PYTHON")
		if python == "" {
			python = "python"
		}
		directory = t.TempDir()
		cmd = exec.CommandContext(ctx, python, "-B", filepath.Join("testdata", "mvp-plugin.py"), directory, "--role", role)
	}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: feasibilityHandshake,
		Plugins:         plugin.PluginSet{role: feasibilityRolePlugin{}}, Cmd: cmd,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: 5 * time.Second,
		Logger: hclog.NewNullLogger(), Stderr: os.Stderr,
	})
	t.Cleanup(func() { feasibilityKill(t, client) })
	rpcClient, err := client.Client()
	if err != nil {
		t.Fatalf("start %s %s: %v", language, role, err)
	}
	if client.Protocol() != plugin.ProtocolGRPC {
		t.Fatal("gRPC not selected")
	}
	raw, err := rpcClient.Dispense(role)
	if err != nil {
		t.Fatal(err)
	}
	conn := raw.(*grpc.ClientConn)
	health, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: plugin.GRPCServiceName})
	if err != nil || health.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("standard health: %v %v", health, err)
	}
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, feasibilitySessionMethod)
	if err != nil {
		t.Fatal(err)
	}
	host := &feasibilityHost{client: client, cmd: cmd, stream: stream, started: started, directory: directory}
	host.receive(t, feasibilityFrame{Version: 2, Type: "ready", Role: role})
	return host
}

func (h *feasibilityHost) receive(t *testing.T, expected feasibilityFrame) {
	t.Helper()
	var message wrapperspb.BytesValue
	if err := h.stream.RecvMsg(&message); err != nil {
		t.Fatal(err)
	}
	var actual feasibilityFrame
	if err := json.Unmarshal(message.Value, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("received %+v; want %+v; error=%v", actual, expected, err)
	}
	t.Logf("Session received: %s", message.Value)
}

func (h *feasibilityHost) stop(t *testing.T) {
	t.Helper()
	cleanupStarted := time.Now()
	feasibilityKill(t, h.client)
	if !h.client.Exited() || h.cmd.ProcessState == nil || !h.cmd.ProcessState.Success() {
		t.Fatalf("standard controller exit not confirmed: exited=%v state=%v", h.client.Exited(), h.cmd.ProcessState)
	}
	t.Logf("one direct child PID=%d; standard Kill/controller exit; elapsed=%s cleanup=%s", h.cmd.Process.Pid, time.Since(h.started), time.Since(cleanupStarted))
}

func TestPluginSessionFeasibility(t *testing.T) {
	for _, language := range []string{"Go", "Python"} {
		for _, role := range []string{"input", "output"} {
			t.Run(language+"/"+role, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				h := startFeasibilityHost(t, ctx, language, role)
				accepted, rejected := true, false
				if role == "input" {
					contextValue := "Immediate event: 日本語\nquoted \"value\" C:\\probe"
					args, _ := json.Marshal(map[string]any{"immediate": contextValue})
					watch := feasibilityFrame{Version: 2, Type: "watch", RequestID: "opaque request 日本語", SubscriptionID: "opaque subscription α", WatchArgs: args}
					if err := feasibilitySend(h.stream, watch); err != nil {
						t.Fatal(err)
					}
					h.receive(t, feasibilityFrame{Version: 2, Type: "watch_result", RequestID: watch.RequestID, Accepted: &accepted})
					h.receive(t, feasibilityFrame{Version: 2, Type: "event", SubscriptionID: watch.SubscriptionID, Context: contextValue})
					watch.RequestID, watch.WatchArgs = "rejected watch", json.RawMessage(`{"reject":true}`)
					if err := feasibilitySend(h.stream, watch); err != nil {
						t.Fatal(err)
					}
					h.receive(t, feasibilityFrame{Version: 2, Type: "watch_result", RequestID: watch.RequestID, Accepted: &rejected, Error: "fixture rejected arguments"})
				} else {
					for _, reject := range []bool{false, true} {
						args, _ := json.Marshal(map[string]any{"reject": reject})
						requestID := "accepted destination 日本語"
						if reject {
							requestID = "rejected destination 日本語"
						}
						request := feasibilityFrame{Version: 2, Type: "validate_destination", RequestID: requestID, DestinationArgs: args}
						if err := feasibilitySend(h.stream, request); err != nil {
							t.Fatal(err)
						}
						ok := !reject
						expected := feasibilityFrame{Version: 2, Type: "destination_result", RequestID: request.RequestID, Accepted: &ok}
						if reject {
							expected.Error = "fixture rejected arguments"
						}
						h.receive(t, expected)
					}
					for _, outcome := range []string{"accepted", "failed", "unknown"} {
						request := feasibilityDelivery(outcome)
						if err := feasibilitySend(h.stream, request); err != nil {
							t.Fatal(err)
						}
						expected := feasibilityFrame{Version: 2, Type: "delivery_result", RequestID: request.RequestID, Status: outcome}
						if outcome != "accepted" {
							expected.Error = "fixture delivery outcome"
						}
						h.receive(t, expected)
						h.assertDeliveryEnvelope(t, request)
					}
				}
				// CloseSend terminates Go's reader; the Python peer can await the
				// framework controller after client cancellation without a peer EOF.
				if err := h.stream.CloseSend(); err != nil {
					t.Fatal(err)
				}
				h.stop(t)
			})
		}
	}
}

func feasibilityDelivery(outcome string) feasibilityFrame {
	args, _ := json.Marshal(map[string]any{"status": outcome})
	return feasibilityFrame{
		Version: 2, Type: "deliver", RequestID: "delivery request " + outcome,
		DeliveryID: "opaque delivery α " + outcome, InputPlugin: "input 日本語", SubscriptionID: "subscription α",
		DestinationArgs: args, Context: "External context 日本語\nquoted \"data\"",
		ExternalData: true, PermissionNotice: "External data; no additional permissions are granted.",
	}
}

func (h *feasibilityHost) assertDeliveryEnvelope(t *testing.T, expected feasibilityFrame) {
	t.Helper()
	if h.directory == "" {
		return
	} // The Go server validates the same fields.
	file, err := os.Open(filepath.Join(h.directory, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	var received map[string]any
	for {
		var row struct {
			Kind  string         `json:"kind"`
			Frame map[string]any `json:"frame"`
		}
		if err := decoder.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if row.Kind == "deliver" {
			received = row.Frame
		}
	}
	data, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("Python received altered envelope: got=%v want=%v", received, want)
	}
	t.Log("standalone output journal confirms unchanged IDs, source, destination arguments, context and external-data notice")
}

func (h *feasibilityHost) awaitDeliveryReceipt(t *testing.T, expected feasibilityFrame) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		file, err := os.Open(filepath.Join(h.directory, "journal.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(file)
		found := false
		for {
			var row struct {
				Kind  string           `json:"kind"`
				Frame feasibilityFrame `json:"frame"`
			}
			if err := decoder.Decode(&row); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					break
				}
				file.Close()
				t.Fatal(err)
			}
			if row.Kind == "deliver" && row.Frame.RequestID == expected.RequestID {
				found = true
			}
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if found {
			h.assertDeliveryEnvelope(t, expected)
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("output peer did not receive the held request")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPluginDeliveryDeadlineFeasibility(t *testing.T) {
	// A real 30-second output Session deadline, not a Codex child timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	h := startFeasibilityHost(t, ctx, "Python", "output")
	request := feasibilityDelivery("accepted")
	request.DestinationArgs = json.RawMessage(`{"hold":true}`)
	started := time.Now()
	deliveryCtx, deliveryCancel := context.WithDeadline(ctx, started.Add(30*time.Second))
	defer deliveryCancel()
	if err := feasibilitySend(h.stream, request); err != nil {
		t.Fatal(err)
	}
	h.awaitDeliveryReceipt(t, request)
	waited := make(chan error, 1)
	go func() { var message wrapperspb.BytesValue; waited <- h.stream.RecvMsg(&message) }()
	select {
	case err := <-waited:
		t.Fatalf("held output confirmed prematurely: %v", err)
	case <-deliveryCtx.Done():
	}
	// The daemon's future timeout invalidates the output instance using Kill.
	h.stop(t)
	select {
	case err := <-waited:
		if err == nil {
			t.Fatal("held output unexpectedly returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not terminate after Kill")
	}
	elapsed := time.Since(started)
	if elapsed < 30*time.Second || elapsed >= 35*time.Second {
		t.Fatalf("delivery deadline/cleanup elapsed=%s outside [30s,35s)", elapsed)
	}
	t.Logf("standalone output peer: request sent once, no result at real 30s deadline, Kill and receive termination; elapsed=%s; no daemon result classification claimed", elapsed)
}

func feasibilityKill(t *testing.T, client *plugin.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() { client.Kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
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
	conflict, err := net.Listen("tcp4", listener.Addr().String())
	if err == nil {
		conflict.Close()
		t.Fatal("second listener acquired the same endpoint")
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
	request := map[string]any{"version": float64(2), "op": "register", "input_plugin": "input", "output_plugin": "output", "watch_args": map[string]any{}, "destination_args": map[string]any{"output_file": "opaque path"}}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, response) {
		t.Fatalf("roundtrip got=%v want=%v", response, request)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := rebound.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("loopback role registration JSON, occupied-port failure and listener release; zero children; elapsed=%s", time.Since(started))
}
