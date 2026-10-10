//go:build windows

package manualplugin

import (
	"bufio"
	"io"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
)

func TestRunAnnouncesReadyAndStopsOnShutdown(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	result := make(chan error, 1)
	go func() { result <- Run(inputReader, outputWriter, io.Discard) }()
	reader := bufio.NewReader(outputReader)
	ready, err := pluginprotocol.Read(reader)
	if err != nil || ready.Type != "ready" {
		t.Fatalf("initial frame = %#v, %v; want ready", ready, err)
	}
	if err := pluginprotocol.Write(inputWriter, pluginprotocol.Frame{Type: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	_ = inputWriter.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual plugin did not stop after shutdown")
	}
	_ = inputReader.Close()
	_ = outputReader.Close()
	_ = outputWriter.Close()
}

func TestRunTreatsInputEOFAsNormalShutdown(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	result := make(chan error, 1)
	go func() { result <- Run(inputReader, outputWriter, io.Discard) }()
	ready, err := pluginprotocol.Read(bufio.NewReader(outputReader))
	if err != nil || ready.Type != "ready" {
		t.Fatalf("initial frame = %#v, %v; want ready", ready, err)
	}
	_ = inputWriter.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("manual plugin did not stop after input EOF")
	}
	_ = inputReader.Close()
	_ = outputReader.Close()
	_ = outputWriter.Close()
}
