package manualplugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

func TestManualPluginConsumesTriggerAndEmitsEvent(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	var stderr bytes.Buffer
	server := NewServer(inputReader, outputWriter, &stderr)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run() }()

	output := bufio.NewReader(outputReader)
	readLine := func() []byte {
		t.Helper()
		return readFrameTimeout(t, output)
	}
	ready, err := protocol.DecodePluginFrame(readLine())
	if err != nil || ready.Type != "ready" {
		t.Fatalf("ready frame = %+v, error = %v", ready, err)
	}

	directory := workspaceTempDir(t)
	triggerPath := filepath.Join(directory, "review trigger.json")
	watchArgs, _ := json.Marshal(map[string]string{"trigger_file": triggerPath})
	if err := writeFrame(inputWriter, protocol.WatchFrame{Version: 1, Type: "watch", RequestID: "request-1", SubscriptionID: "subscription-1", WatchArgs: watchArgs}); err != nil {
		t.Fatal(err)
	}
	accepted, err := protocol.DecodePluginFrame(readLine())
	if err != nil || accepted.Type != "watch_result" || !accepted.Accepted {
		t.Fatalf("watch response = %+v, error = %v", accepted, err)
	}

	context := "review comment 42 is ready"
	temporary, err := os.CreateTemp(directory, ".trigger-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := temporary.WriteString(context); err != nil {
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary.Name(), triggerPath); err != nil {
		t.Fatal(err)
	}
	frame, err := protocol.DecodePluginFrame(readLine())
	if err != nil || frame.Type != "event" || frame.SubscriptionID != "subscription-1" || frame.Context != context {
		t.Fatalf("event frame = %+v, error = %v", frame, err)
	}
	if _, err := os.Stat(triggerPath); !os.IsNotExist(err) {
		t.Fatalf("trigger file still exists or could not be checked: %v", err)
	}

	if _, err := inputWriter.Write([]byte("{\"version\":1,\"type\":\"shutdown\"}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual plugin did not stop")
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
}

func TestManualPluginRejectsDuplicateNormalizedPath(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	server := NewServer(inputReader, outputWriter, io.Discard)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run() }()
	output := bufio.NewReader(outputReader)
	_ = readFrameTimeout(t, output)

	path := filepath.Join(t.TempDir(), "trigger")
	args, _ := json.Marshal(map[string]string{"trigger_file": path})
	firstPath, _ := json.Marshal(map[string]string{"trigger_file": path})
	if err := writeFrame(inputWriter, protocol.WatchFrame{Version: 1, Type: "watch", RequestID: "first", SubscriptionID: "sub-1", WatchArgs: firstPath}); err != nil {
		t.Fatal(err)
	}
	first := readFrameTimeout(t, output)
	firstResult, err := protocol.DecodePluginFrame(first)
	if err != nil || !firstResult.Accepted {
		t.Fatalf("first watch rejected: %+v, %v", firstResult, err)
	}
	if err := writeFrame(inputWriter, protocol.WatchFrame{Version: 1, Type: "watch", RequestID: "second", SubscriptionID: "sub-2", WatchArgs: args}); err != nil {
		t.Fatal(err)
	}
	second := readFrameTimeout(t, output)
	secondResult, err := protocol.DecodePluginFrame(second)
	if err != nil || secondResult.Accepted || secondResult.Error == "" {
		t.Fatalf("duplicate watch response = %+v, error = %v", secondResult, err)
	}
	_ = writeFrame(inputWriter, protocol.ShutdownFrame{Version: 1, Type: "shutdown"})
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("manual plugin did not stop")
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
}

func TestManualPluginContinuesAfterInvalidUTF8Frame(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	server := NewServer(inputReader, outputWriter, io.Discard)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run() }()
	output := bufio.NewReader(outputReader)
	_ = readFrameTimeout(t, output)

	if _, err := inputWriter.Write([]byte{0xff, '\n'}); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(inputWriter, protocol.ShutdownFrame{Version: 1, Type: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server shutdown after malformed frame: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual plugin did not read shutdown after invalid UTF-8 frame")
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
}

func writeFrame(writer io.Writer, frame any) error {
	line, err := protocol.EncodeLine(frame)
	if err != nil {
		return err
	}
	_, err = writer.Write(line)
	return err
}

func readFrameTimeout(t *testing.T, reader *bufio.Reader) []byte {
	t.Helper()
	result := make(chan frameReadResult, 1)
	go func() {
		data, err := protocol.ReadFrame(reader, pulse.MaxFrameBytes)
		result <- frameReadResult{data: data, err: err}
	}()
	select {
	case value := <-result:
		if value.err != nil {
			t.Fatalf("read plugin frame: %v", value.err)
		}
		return value.data
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for plugin frame")
		return nil
	}
}

func workspaceTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "manual-plugin-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

type frameReadResult struct {
	data []byte
	err  error
}
