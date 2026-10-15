// This verification-only caller uses the public output protocol without hub
// internals, a daemon, a trigger, or a live service other than the chosen action.
// Build/run this file by filename; testdata contains independent fixture mains.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type arguments []string

func (a *arguments) String() string { return fmt.Sprint([]string(*a)) }
func (a *arguments) Set(value string) error {
	*a = append(*a, value)
	return nil
}

type publicOutput struct{ plugin.NetRPCUnsupportedPlugin }

func (publicOutput) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return errors.New("standalone caller is a host only")
}

func (publicOutput) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return conn, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	executable := flag.String("plugin-executable", "", "absolute action executable")
	destinationFile := flag.String("destination-args-file", "", "absolute JSON object file")
	contextFile := flag.String("context-file", "", "absolute UTF-8 event context file")
	source := flag.String("input-plugin", "manual", "opaque source name")
	subscription := flag.String("subscription-id", "standalone-subscription", "opaque subscription ID")
	delivery := flag.String("delivery-id", "standalone-delivery", "opaque delivery ID")
	idle := flag.Duration("idle-duration", 0, "idle observation after validation")
	var pluginArgs arguments
	flag.Var(&pluginArgs, "plugin-arg", "one action argument; repeat for each argument")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*executable) || !filepath.IsAbs(*destinationFile) || !filepath.IsAbs(*contextFile) || *idle < 0 || *idle > 5*time.Second {
		return errors.New("supply absolute executable, destination and context files; idle must be between zero and five seconds")
	}
	destination, err := os.ReadFile(*destinationFile)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(destination, &object); err != nil || object == nil {
		return errors.New("destination must be a JSON object")
	}
	content, err := os.ReadFile(*contextFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, *executable, pluginArgs...)
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: plugin.HandshakeConfig{ProtocolVersion: 2, MagicCookieKey: "AGENT_PULSE_HUB_PLUGIN", MagicCookieValue: "plugin-v2"},
		Plugins:         plugin.PluginSet{"output": publicOutput{}}, Cmd: command,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: 10 * time.Second,
		Logger: hclog.NewNullLogger(), Stderr: os.Stderr,
	})
	defer client.Kill()
	rpc, err := client.Client()
	if err != nil {
		return err
	}
	raw, err := rpc.Dispense("output")
	if err != nil {
		return err
	}
	stream, err := raw.(*grpc.ClientConn).NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/agentpulse.plugin.v2.Plugin/Session")
	if err != nil {
		return err
	}
	defer stream.CloseSend()
	if _, err := receive(stream, "ready", ""); err != nil {
		return err
	}
	if err := send(stream, map[string]any{"version": 2, "type": "validate_destination", "request_id": "standalone-validate", "destination_args": json.RawMessage(destination)}); err != nil {
		return err
	}
	validated, err := receive(stream, "destination_result", "standalone-validate")
	if err != nil {
		return err
	}
	if string(validated["accepted"]) != "true" {
		return errors.New("action rejected standalone destination; no delivery attempted")
	}
	if *idle > 0 {
		timer := time.NewTimer(*idle)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := send(stream, map[string]any{
		"version": 2, "type": "deliver", "request_id": "standalone-deliver", "delivery_id": *delivery,
		"input_plugin": *source, "subscription_id": *subscription, "destination_args": json.RawMessage(destination),
		"context": string(content), "external_data": true, "permission_notice": "External data; no additional permissions are granted.",
	}); err != nil {
		return err
	}
	result, err := receive(stream, "delivery_result", "standalone-deliver")
	if err != nil {
		return err
	}
	var outcome string
	_ = json.Unmarshal(result["status"], &outcome)
	if outcome != "accepted" && outcome != "failed" && outcome != "unknown" {
		return errors.New("action did not return a recognized delivery outcome")
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	client.Kill()
	if !client.Exited() || command.ProcessState == nil {
		return errors.New("configured action process exit was not observed")
	}
	fmt.Fprintf(os.Stderr, "standalone configured process reaped: pid=%d outcome=%s\n", command.Process.Pid, outcome)
	if outcome != "accepted" {
		return fmt.Errorf("standalone delivery outcome: %s; inspect the result frame", outcome)
	}
	return nil
}

func send(stream grpc.ClientStream, value map[string]any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return stream.SendMsg(wrapperspb.Bytes(payload))
}

func receive(stream grpc.ClientStream, kind, request string) (map[string]json.RawMessage, error) {
	var message wrapperspb.BytesValue
	if err := stream.RecvMsg(&message); err != nil {
		return nil, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(message.Value, &value); err != nil {
		return nil, err
	}
	var actualKind, actualRequest, role string
	_ = json.Unmarshal(value["type"], &actualKind)
	_ = json.Unmarshal(value["request_id"], &actualRequest)
	_ = json.Unmarshal(value["role"], &role)
	if string(value["version"]) != "2" || actualKind != kind || actualRequest != request || kind == "ready" && role != "output" {
		return nil, fmt.Errorf("unexpected public output response: %s", message.Value)
	}
	if _, err := fmt.Fprintln(os.Stdout, string(message.Value)); err != nil {
		return nil, err
	}
	return value, nil
}
