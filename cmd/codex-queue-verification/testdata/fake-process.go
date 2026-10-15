// This controlled external-process fixture is built by filename. It is not a
// Codex implementation and establishes no compatibility with a real service.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type control struct {
	Kind    string `json:"kind"`
	Mode    string `json:"mode"`
	Version string `json:"version"`
}

func main() {
	settingsBytes, err := os.ReadFile(os.Getenv("APH_CQ_FIXTURE_CONTROL"))
	if err != nil {
		fail(err)
	}
	var settings control
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		fail(err)
	}
	args := os.Args[1:]
	kind := settings.Kind
	if kind == "codex" && len(args) == 1 && args[0] == "--version" {
		if settings.Version == "" {
			settings.Version = "codex-cli 0.120.0-fixture"
		}
		fmt.Println(settings.Version)
		return
	}
	if kind == "codex" && len(args) == 2 && args[0] == "queue" && args[1] == "--help" {
		fmt.Println("Usage: codex queue --thread <THREAD_ID> --message <MESSAGE>")
		return
	}
	row := map[string]any{
		"kind": kind, "mode": settings.Mode, "argv": args,
		"pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if kind == "client" {
		path := argument(args, "--destination-args-file")
		value, readErr := os.ReadFile(path)
		if readErr != nil {
			fail(readErr)
		}
		row["destination_path"] = path
		row["destination"] = json.RawMessage(value)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		fail(err)
	}
	journal, err := os.OpenFile(os.Getenv("APH_CQ_FIXTURE_JOURNAL"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err)
	}
	if _, err := journal.Write(append(encoded, '\n')); err != nil {
		fail(err)
	}
	if err := journal.Close(); err != nil {
		fail(err)
	}
	switch settings.Mode {
	case "hold":
		for {
			time.Sleep(time.Hour)
		}
	case "truncated":
		fmt.Print(`{"ok":true,"subscription_id":`)
		return
	case "rejected":
		fmt.Println(`{"ok":false,"code":"destination_rejected","message":"controlled rejection"}`)
		os.Exit(1)
	case "accepted", "mismatched":
		if kind == "client" {
			fmt.Println(`{"ok":true,"subscription_id":"cq-sub-α"}`)
			return
		}
		if kind != "codex" || len(args) != 5 || args[0] != "queue" || args[1] != "--thread" || args[3] != "--message" {
			fail(fmt.Errorf("unexpected controlled CLI arguments: %q", args))
		}
		target := args[2]
		if settings.Mode == "mismatched" {
			target = "22222222-2222-4222-8222-222222222222"
		}
		fmt.Printf("Queued message 33333333-3333-4333-8333-333333333333 for thread %s.\n", target)
	default:
		fail(fmt.Errorf("unsupported fixture mode %q", settings.Mode))
	}
}

func argument(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}
