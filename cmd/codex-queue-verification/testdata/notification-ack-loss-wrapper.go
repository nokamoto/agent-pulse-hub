// Delivery-only fault fixture, built by filename. This is not a product action
// or a Codex implementation. A successful queue ACK is saved privately and
// withheld from the caller to qualify the selected response-loss stimulus.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const captureLimit = 64 * 1024

type boundedCapture struct {
	value    bytes.Buffer
	overflow bool
}

func (b *boundedCapture) Write(value []byte) (int, error) {
	remaining := captureLimit - b.value.Len()
	if remaining < len(value) {
		b.overflow = true
	}
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		_, _ = b.value.Write(value[:remaining])
	}
	return len(value), nil
}

func (b *boundedCapture) Bytes() []byte  { return b.value.Bytes() }
func (b *boundedCapture) String() string { return b.value.String() }
func (b *boundedCapture) Len() int       { return b.value.Len() }

var acknowledgement = regexp.MustCompile(`^Queued message [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12} for thread ([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.\r?\n?$`)

func main() {
	os.Exit(run())
}

func run() int {
	arguments := os.Args[1:]
	queue := len(arguments) == 5 && arguments[0] == "queue" && arguments[1] == "--thread" && arguments[3] == "--message"
	prerequisite := len(arguments) == 1 && (arguments[0] == "--version" || arguments[0] == "--help") || len(arguments) == 2 && arguments[0] == "queue" && arguments[1] == "--help"
	if !queue && !prerequisite {
		return diagnostic("unsupported fixture invocation")
	}
	realPath := os.Getenv("APH_ND_REAL_CODEX")
	ackPath := os.Getenv("APH_ND_ACK_FILE")
	if !filepath.IsAbs(realPath) || queue && !filepath.IsAbs(ackPath) {
		return diagnostic("absolute executable and private evidence paths are required")
	}
	selfPath, err := os.Executable()
	if err != nil {
		return diagnostic("cannot identify fixture executable")
	}
	selfInfo, selfErr := os.Stat(selfPath)
	realInfo, realErr := os.Stat(realPath)
	if selfErr != nil || realErr != nil || os.SameFile(selfInfo, realInfo) {
		return diagnostic("invalid destination executable")
	}
	// The fixture's shorter bound leaves time to reap its direct child before
	// the real action's unchanged 20-second deadline can kill this wrapper.
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, realPath, arguments...)
	command.WaitDelay = time.Second
	var stdout, stderr boundedCapture
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stdout.overflow || stderr.overflow {
		return diagnostic("controlled child did not complete with bounded successful output")
	}
	if prerequisite {
		if _, err := os.Stdout.Write(stdout.Bytes()); err != nil {
			return diagnostic("cannot forward prerequisite output")
		}
		return 0
	}
	match := acknowledgement.FindStringSubmatch(stdout.String())
	if len(match) != 2 || match[1] != arguments[2] || strings.TrimSpace(stderr.String()) != "" {
		return diagnostic("successful matching target acknowledgement is unavailable")
	}
	if info, err := os.Lstat(ackPath); err == nil {
		if !info.Mode().IsRegular() {
			return diagnostic("private acknowledgement target must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return diagnostic("cannot inspect private acknowledgement target")
	}
	// Append every successful ACK. This file must never fence an invocation or
	// accidentally provide deduplication absent from the product or destination.
	file, err := os.OpenFile(ackPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return diagnostic("cannot open private acknowledgement evidence")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return diagnostic("cannot restrict private acknowledgement evidence")
	}
	written, writeErr := file.Write(stdout.Bytes())
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil || written != stdout.Len() {
		return diagnostic("cannot commit private acknowledgement evidence")
	}
	// The selected fault: successful real target ACK is intentionally hidden.
	return 1
}

func diagnostic(message string) int {
	// Never print process arguments, event context, credentials or raw output.
	_, _ = fmt.Fprintln(os.Stderr, "ACK-loss fixture: "+message)
	return 2
}
