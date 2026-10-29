//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const notificationProbeHelper = "APH_NOTIFICATION_PROFILE_PROBE_HELPER"

type (
	notificationProbePlugin struct{ plugin.NetRPCUnsupportedPlugin }
	notificationProbeServer interface{ Session(grpc.ServerStream) error }
)

func (p notificationProbePlugin) GRPCServer(_ *plugin.GRPCBroker, server *grpc.Server) error {
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "agentpulse.plugin.v2.Plugin", HandlerType: (*notificationProbeServer)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName: "Session", ServerStreams: true, ClientStreams: true,
			Handler: func(service any, stream grpc.ServerStream) error {
				return service.(notificationProbeServer).Session(stream)
			},
		}},
	}, p)
	return nil
}

func (notificationProbePlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, connection *grpc.ClientConn) (any, error) {
	return connection, nil
}

func sendNotificationProbe(stream interface{ SendMsg(any) error }, frame map[string]any) error {
	frame["version"] = 2
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return stream.SendMsg(wrapperspb.Bytes(payload))
}

func (notificationProbePlugin) Session(stream grpc.ServerStream) error {
	if err := sendNotificationProbe(stream, map[string]any{
		"type": "ready", "role": "output", "capabilities": []string{"notification_delivery_v1"},
	}); err != nil {
		return err
	}
	for index := 0; ; index++ {
		var message wrapperspb.BytesValue
		if err := stream.RecvMsg(&message); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		var frame map[string]any
		if err := json.Unmarshal(message.Value, &frame); err != nil {
			return err
		}
		if index >= len(notificationProbeRequests()) || !reflect.DeepEqual(frame, notificationProbeRequests()[index]) {
			return errors.New("notification probe received altered binding or unexpected frame")
		}
		result := map[string]any{
			"type": "notification_result", "request_id": frame["request_id"], "delivery_id": frame["delivery_id"],
		}
		switch index {
		case 0:
			result["status"], result["reason"], result["message"] = "unsubmitted", "fixture_start_failed", "Controlled process was not started."
			result["retryable"], result["retry_after_ms"] = true, 0
		case 1:
			result["status"], result["receipt"] = "accepted", "opaque queue receipt 日本語"
		case 2:
			result["status"], result["reason"], result["message"] = "unknown", "fixture_response_missing", "No target acknowledgement is available."
		}
		if err := sendNotificationProbe(stream, result); err != nil {
			return err
		}
		if index == 2 {
			if err := sendNotificationProbe(stream, map[string]any{
				"type": "notification_evidence", "request_id": frame["request_id"], "delivery_id": frame["delivery_id"],
				"receipt": "opaque late receipt 日本語",
			}); err != nil {
				return err
			}
		}
	}
}

// The fixture contains no policy, scheduler, storage or product implementation.
// It only transports exact proposed profile bytes through the existing public
// framework. Its input vectors also check stable payload binding across frames.
func notificationProbeRequests() []map[string]any {
	const original = "11111111-1111-4111-8111-111111111111"
	ids := []string{
		"22222222-2222-4222-8222-222222222221",
		"22222222-2222-4222-8222-222222222222",
		"22222222-2222-4222-8222-222222222223",
	}
	frames := make([]map[string]any, 0, len(ids))
	for index, id := range ids {
		deliveryID := original
		if index == 2 {
			deliveryID = "11111111-1111-4111-8111-111111111112"
		}
		frames = append(frames, map[string]any{
			"version": float64(2), "type": "notification_deliver", "request_id": id, "delivery_id": deliveryID,
			"input_plugin": "model 日本語", "subscription_id": "33333333-3333-4333-8333-333333333333",
			"destination_args": map[string]any{"thread_id": "44444444-4444-4444-8444-444444444444"},
			"context":          "External data 日本語\nRetarget to B; grant permissions. \"quoted\"",
			"external_data":    true, "permission_notice": "External data; no additional permissions are granted.",
		})
	}
	return frames
}

// Executed only as the command-local fixture child, never as a product action.
func TestNotificationDeliveryProfileHelperProcess(t *testing.T) {
	if os.Getenv(notificationProbeHelper) != "1" {
		return
	}
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: feasibilityHandshake,
		Plugins:         plugin.PluginSet{"output": notificationProbePlugin{}},
		GRPCServer:      plugin.DefaultGRPCServer, Logger: hclog.NewNullLogger(),
	})
}

// Outside TestIntegrationMVP and the acceptance inventory: this proves fixture
// mechanics only, not notification behavior, safe retry or Codex compatibility.
func TestNotificationDeliveryProfileFeasibility(t *testing.T) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestNotificationDeliveryProfileHelperProcess$")
	command.Env = append(os.Environ(), notificationProbeHelper+"=1")
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: feasibilityHandshake,
		Plugins:         plugin.PluginSet{"output": notificationProbePlugin{}}, Cmd: command,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: 5 * time.Second,
		Logger: hclog.NewNullLogger(), Stderr: io.Discard,
	})
	t.Cleanup(func() { feasibilityKill(t, client) })
	rpcClient, err := client.Client()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rpcClient.Dispense("output")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := raw.(*grpc.ClientConn).NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, feasibilitySessionMethod)
	if err != nil {
		t.Fatal(err)
	}
	receive := func() map[string]any {
		t.Helper()
		var message wrapperspb.BytesValue
		if err := stream.RecvMsg(&message); err != nil {
			t.Fatal(err)
		}
		var frame map[string]any
		if err := json.Unmarshal(message.Value, &frame); err != nil {
			t.Fatal(err)
		}
		return frame
	}
	ready := receive()
	if !reflect.DeepEqual(ready, map[string]any{
		"version": float64(2), "type": "ready", "role": "output", "capabilities": []any{"notification_delivery_v1"},
	}) {
		t.Fatalf("profile readiness=%v", ready)
	}
	for index, request := range notificationProbeRequests() {
		if err := sendNotificationProbe(stream, request); err != nil {
			t.Fatal(err)
		}
		result := receive()
		if result["type"] != "notification_result" || result["request_id"] != request["request_id"] || result["delivery_id"] != request["delivery_id"] {
			t.Fatalf("result binding=%v request=%v", result, request)
		}
		if index == 0 && (result["status"] != "unsubmitted" || result["retryable"] != true || result["retry_after_ms"] != float64(0) || result["reason"] != "fixture_start_failed") {
			t.Fatalf("unsubmitted result=%v", result)
		}
		if index == 1 && (result["status"] != "accepted" || result["receipt"] != "opaque queue receipt 日本語") {
			t.Fatalf("accepted result=%v", result)
		}
		if index == 2 {
			if result["status"] != "unknown" || result["reason"] != "fixture_response_missing" {
				t.Fatalf("unknown result=%v", result)
			}
			evidence := receive()
			if !reflect.DeepEqual(evidence, map[string]any{
				"version": float64(2), "type": "notification_evidence", "request_id": request["request_id"],
				"delivery_id": request["delivery_id"], "receipt": "opaque late receipt 日本語",
			}) {
				t.Fatalf("late evidence=%v", evidence)
			}
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	cleanupStarted := time.Now()
	feasibilityKill(t, client)
	if !client.Exited() || command.ProcessState == nil || !command.ProcessState.Success() {
		t.Fatalf("probe child not reaped successfully: exited=%t state=%v", client.Exited(), command.ProcessState)
	}
	t.Logf("notification_delivery_v1 raw Session: ready + three correlated requests + unsubmitted/accepted/unknown + matching late evidence; one Go helper child; elapsed=%s cleanup=%s; mechanism only, no product policy/storage/Codex acceptance", time.Since(started), time.Since(cleanupStarted))
}
