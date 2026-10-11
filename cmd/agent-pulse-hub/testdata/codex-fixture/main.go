package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	root := os.Getenv("APH_FIXTURE_ROOT")
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		appendRecord(filepath.Join(root, "versions.jsonl"), map[string]any{"pid": os.Getpid(), "args": os.Args[1:]})
		fmt.Println("codex-cli integration-fixture")
		return
	}
	if len(os.Args) != 6 || os.Args[1] != "queue" || os.Args[2] != "--thread" || os.Args[4] != "--message" {
		os.Exit(2)
	}
	mode, _ := os.ReadFile(filepath.Join(root, "mode"))
	appendRecord(filepath.Join(root, "calls.jsonl"), map[string]any{"pid": os.Getpid(), "args": os.Args[1:], "mode": string(mode), "at": time.Now().UTC()})
	switch string(mode) {
	case "nonzero":
		os.Exit(3)
	case "held":
		for {
			time.Sleep(time.Hour)
		}
	default:
		fmt.Printf("Queued message 22222222-2222-4222-8222-222222222222 for thread %s.\n", os.Args[3])
	}
}

func appendRecord(path string, value any) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		panic(err)
	}
	if err := file.Close(); err != nil {
		panic(err)
	}
}
