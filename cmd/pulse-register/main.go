package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/controlpipe"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("pulse-register", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	plugin := flags.String("plugin", "", "configured plugin name")
	sessionID := flags.String("session-id", "", "UUID of the current Codex conversation")
	watchArgs := flags.String("watch-args", "", "JSON object containing plugin watch arguments")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *plugin == "" || *sessionID == "" || *watchArgs == "" {
		return errors.New("usage: pulse-register --plugin <name> --session-id <UUID> --watch-args <JSON object>")
	}
	if err := validateCurrentSession(*sessionID); err != nil {
		return err
	}
	request := protocol.ControlRequest{
		Version:   protocol.Version,
		Operation: "register",
		Plugin:    *plugin,
		SessionID: *sessionID,
		WatchArgs: json.RawMessage(*watchArgs),
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode registration request: %w", err)
	}
	if _, err := protocol.DecodeControlRequest(encodedRequest); err != nil {
		return fmt.Errorf("invalid registration request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := controlpipe.Register(ctx, request)
	if err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("registration failed [%s]: %s", response.Code, response.Message)
	}
	fmt.Println(response.SubscriptionID)
	return nil
}

func validateCurrentSession(sessionID string) error {
	threadID := os.Getenv("CODEX_THREAD_ID")
	currentSessionID := os.Getenv("CODEX_SESSION_ID")
	if threadID == "" || currentSessionID == "" {
		return errors.New("current session identity is unavailable; run this skill in the top-level Codex conversation")
	}
	if pulse.ValidateUUID(threadID) != nil || pulse.ValidateUUID(currentSessionID) != nil || !strings.EqualFold(threadID, currentSessionID) {
		return errors.New("CODEX_THREAD_ID and CODEX_SESSION_ID must be valid matching UUIDs")
	}
	if !strings.EqualFold(sessionID, threadID) {
		return errors.New("session-id must match the current CODEX_THREAD_ID")
	}
	return nil
}
