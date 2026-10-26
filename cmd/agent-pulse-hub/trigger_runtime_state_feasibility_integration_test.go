//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Standalone mechanism probes are deliberately outside TestIntegrationMVP and
// the scoped case inventory. Neither test proves hub runtime-state behavior.
func TestTriggerRuntimeStateProtocolFeasibility(t *testing.T) {
	probeRuntimeStateProtocol(t, false)
}

func TestTriggerRuntimeStateActivationFeasibility(t *testing.T) {
	probeRuntimeStateProtocol(t, true)
}

func probeRuntimeStateProtocol(t *testing.T, activationV2 bool) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	python := os.Getenv("APH_PLUGIN_PYTHON")
	if python == "" {
		python = "python"
	}
	command := exec.CommandContext(ctx, python, "-B", filepath.Join("testdata", "trigger-runtime-state-probe.py"))
	if activationV2 {
		command.Env = append(os.Environ(), "APH_PROBE_ACTIVATION=1")
	}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: feasibilityHandshake,
		Plugins:         plugin.PluginSet{"input": feasibilityRolePlugin{}}, Cmd: command,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: 5 * time.Second,
		Logger: hclog.NewNullLogger(), Stderr: os.Stderr,
	})
	t.Cleanup(func() { feasibilityKill(t, client) })
	rpcClient, err := client.Client()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := rpcClient.Dispense("input")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := raw.(*grpc.ClientConn).NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, feasibilitySessionMethod)
	if err != nil {
		t.Fatal(err)
	}
	send := func(frame map[string]any) {
		t.Helper()
		frame["version"] = 2
		payload, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.SendMsg(wrapperspb.Bytes(payload)); err != nil {
			t.Fatal(err)
		}
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
	capability := "runtime_state_v1"
	if activationV2 {
		capability = "runtime_state_v2"
	}
	if ready["type"] != "ready" || !reflect.DeepEqual(ready["capabilities"], []any{capability}) {
		t.Fatalf("capability readiness = %v", ready)
	}
	send(map[string]any{"type": "state_watch", "request_id": "watch 日本語", "subscription_id": "subscription-probe", "watch_args": map[string]any{}, "state": map[string]any{"schema": "probe-v1", "payload": map[string]any{"accepted": []string{"RESTORED"}}}})
	result := receive()
	if result["type"] != "watch_result" || result["request_id"] != "watch 日本語" || result["accepted"] != true {
		t.Fatalf("watch correlation = %v", result)
	}
	if activationV2 {
		// A checkpoint on the same stream is a deterministic barrier: any early
		// event would arrive before its response and fail this observation.
		send(map[string]any{"type": "checkpoint", "request_id": "prepared", "subscription_id": "subscription-probe", "quiesce": false})
		prepared := receive()
		if prepared["type"] != "checkpoint_result" || prepared["request_id"] != "prepared" {
			t.Fatalf("dormant watch emitted before grant: %v", prepared)
		}
		// Invalid ownership cannot activate; the valid duplicate is idempotent.
		send(map[string]any{"type": "activate_watch", "subscription_id": "foreign"})
		send(map[string]any{"type": "checkpoint", "request_id": "foreign-grant", "subscription_id": "subscription-probe", "quiesce": false})
		if frame := receive(); frame["type"] != "checkpoint_result" || frame["request_id"] != "foreign-grant" {
			t.Fatalf("foreign grant activated watch: %v", frame)
		}
		send(map[string]any{"type": "activate_watch", "subscription_id": "subscription-probe"})
		send(map[string]any{"type": "activate_watch", "subscription_id": "subscription-probe"})
	}
	for _, id := range []string{"1", "2"} {
		event := receive()
		if event["type"] != "state_event" || event["subscription_id"] != "subscription-probe" || event["event_id"] != id {
			t.Fatalf("ordered state event %s = %v", id, event)
		}
	}
	// The peer processes notices in stream order before the checkpoint. Unknown
	// acceptance must not appear as confirmed logical progress in the snapshot.
	send(map[string]any{"type": "delivery_notice", "subscription_id": "subscription-probe", "event_id": "1", "status": "accepted"})
	send(map[string]any{"type": "delivery_notice", "subscription_id": "subscription-probe", "event_id": "2", "status": "unknown"})
	send(map[string]any{"type": "checkpoint", "request_id": "stop-checkpoint 日本語", "subscription_id": "subscription-probe", "quiesce": true})
	checkpoint := receive()
	wantState := map[string]any{"schema": "probe-v1", "payload": map[string]any{"accepted": []any{"RESTORED", "PROBE-U1"}}}
	if checkpoint["type"] != "checkpoint_result" || checkpoint["request_id"] != "stop-checkpoint 日本語" || checkpoint["accepted"] != true || !reflect.DeepEqual(checkpoint["state"], wantState) {
		t.Fatalf("notice-before-checkpoint snapshot = %v; want state %v", checkpoint, wantState)
	}
	if activationV2 {
		// The next response also proves that the duplicate grant emitted nothing.
		// A new dormant watch can be quiesced before a late grant reaches it.
		send(map[string]any{"type": "state_watch", "request_id": "watch-dormant", "subscription_id": "dormant", "watch_args": map[string]any{}, "state": nil})
		if frame := receive(); frame["type"] != "watch_result" || frame["request_id"] != "watch-dormant" {
			t.Fatalf("duplicate grant emitted extra work: %v", frame)
		}
		send(map[string]any{"type": "checkpoint", "request_id": "quiesce-dormant", "subscription_id": "dormant", "quiesce": true})
		if frame := receive(); frame["type"] != "checkpoint_result" || frame["request_id"] != "quiesce-dormant" {
			t.Fatalf("dormant quiescence = %v", frame)
		}
		send(map[string]any{"type": "activate_watch", "subscription_id": "dormant"})
		send(map[string]any{"type": "checkpoint", "request_id": "late-grant", "subscription_id": "dormant", "quiesce": false})
		if frame := receive(); frame["type"] != "checkpoint_result" || frame["request_id"] != "late-grant" {
			t.Fatalf("late grant resumed a quiesced watch: %v", frame)
		}
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	cleanupStarted := time.Now()
	feasibilityKill(t, client)
	if !client.Exited() || command.ProcessState == nil || !command.ProcessState.Success() {
		t.Fatalf("configured peer exit = %v, %v", client.Exited(), command.ProcessState)
	}
	if elapsed := time.Since(started); elapsed >= 10*time.Second {
		t.Fatalf("minimal protocol probe exceeded checkpoint batch budget: %s", elapsed)
	}
	t.Logf("runtime-state=%s; OS=%s/%s; one Python framework child; 2 ordered events, accepted/unknown notices, correlated quiesce-request snapshot; total=%s cleanup=%s; mechanism feasibility only, not product acceptance, producer-quiescence verification or a maximum-watch batch measurement", capability, runtime.GOOS, runtime.GOARCH, time.Since(started), time.Since(cleanupStarted))
}

func TestTriggerRuntimeStateFileFeasibility(t *testing.T) {
	started := time.Now()
	root := t.TempDir()
	runtimeDirectory := filepath.Join(root, "runtime", "registration-probe")
	stagingDirectory := filepath.Join(root, "staging")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, generation := range []string{"00000000000000000001", "00000000000000000002"} {
		file, err := os.OpenFile(filepath.Join(stagingDirectory, generation), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte(`{"schema":"probe-v1","payload":{"cursor":"` + generation + `"}}`)
		written, writeErr := file.Write(payload)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil || written != len(payload) {
			t.Fatalf("write/close: %d, %v, %v", written, writeErr, closeErr)
		}
		if err := os.Rename(filepath.Join(stagingDirectory, generation), filepath.Join(runtimeDirectory, generation+".json")); err != nil {
			t.Fatal(err)
		}
	}
	latest := filepath.Join(runtimeDirectory, "00000000000000000002.json")
	before, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(runtimeDirectory, "00000000000000000001.json")); err != nil {
		t.Fatal(err)
	}
	// A regular file in place of a registration's runtime directory forces the
	// same portable failure boundary proposed for TRS-V03, without chmod tricks.
	preserved := filepath.Join(root, "preserved-runtime")
	if err := os.Rename(runtimeDirectory, preserved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeDirectory, []byte("save obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(stagingDirectory, "next")
	if err := os.WriteFile(stage, []byte("uncommitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, filepath.Join(runtimeDirectory, "00000000000000000003.json")); err == nil {
		t.Fatal("obstructed rename unexpectedly succeeded")
	}
	after, err := os.ReadFile(filepath.Join(preserved, filepath.Base(latest)))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("last committed bytes changed: %q, %v", after, err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("failed rename removed uncommitted stage: %v", err)
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(runtimeDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(preserved, runtimeDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDirectory, "00000000000000000003.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed generation appeared committed: %v", err)
	}
	t.Logf("OS=%s/%s; exclusive stage/write/close/unique-generation rename, prior cleanup, directory obstruction and recovery; zero children; elapsed=%s; filesystem feasibility only, not product acceptance", runtime.GOOS, runtime.GOARCH, time.Since(started))
}
