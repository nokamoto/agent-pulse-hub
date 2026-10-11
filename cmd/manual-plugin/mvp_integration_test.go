//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var manualExecutable string

var _ = BeforeSuite(func() {
	Expect(runtime.GOOS).To(Equal("windows"), "MVP acceptance requires Windows")
	started := time.Now()
	directory, err := os.MkdirTemp("", "aph-manual-build-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(os.RemoveAll(directory)).To(Succeed()) })
	manualExecutable = filepath.Join(directory, "manual-plugin.exe")
	buildContext, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	command := exec.CommandContext(buildContext, "go", "build", "-o", manualExecutable, ".")
	output, err := command.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(output))
	AddReportEntry("common build/setup seconds", time.Since(started).Seconds())
	Expect(time.Since(started)).To(BeNumerically("<=", 40*time.Second))
})

type manualFrame struct {
	Version        int    `json:"version"`
	Type           string `json:"type"`
	RequestID      string `json:"request_id"`
	SubscriptionID string `json:"subscription_id"`
	Accepted       *bool  `json:"accepted"`
	Error          string `json:"error"`
	Context        string `json:"context"`
}

type manualTranscript struct {
	sync.Mutex
	frames      []manualFrame
	diagnostics bytes.Buffer
	decodeErr   error
}

func (transcript *manualTranscript) Write(data []byte) (int, error) {
	transcript.Lock()
	defer transcript.Unlock()
	if transcript.diagnostics.Len()+len(data) > 16*1024 {
		return 0, fmt.Errorf("diagnostic capture exceeded bound")
	}
	return transcript.diagnostics.Write(data)
}

func (transcript *manualTranscript) snapshot() ([]manualFrame, string, error) {
	transcript.Lock()
	defer transcript.Unlock()
	return append([]manualFrame(nil), transcript.frames...), transcript.diagnostics.String(), transcript.decodeErr
}

type manualChild struct {
	command    *exec.Cmd
	stdin      io.WriteCloser
	transcript *manualTranscript
	exited     chan struct{}
	readDone   chan struct{}
	waitErr    error
	deadline   time.Time
	stopped    bool
}

func startManualChild(deadline time.Time) *manualChild {
	command := exec.Command(manualExecutable)
	stdin, err := command.StdinPipe()
	Expect(err).NotTo(HaveOccurred())
	reader, writer := io.Pipe()
	transcript := &manualTranscript{}
	command.Stdout, command.Stderr = writer, transcript
	child := &manualChild{command: command, stdin: stdin, transcript: transcript, exited: make(chan struct{}), readDone: make(chan struct{}), deadline: deadline}
	go func() {
		defer close(child.readDone)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 65536)
		for scanner.Scan() {
			var frame manualFrame
			err := json.Unmarshal(scanner.Bytes(), &frame)
			transcript.Lock()
			if err != nil {
				transcript.decodeErr = err
			} else if len(transcript.frames) >= 32 {
				transcript.decodeErr = fmt.Errorf("too many frames")
			} else {
				transcript.frames = append(transcript.frames, frame)
			}
			transcript.Unlock()
		}
		transcript.Lock()
		if scanner.Err() != nil {
			transcript.decodeErr = scanner.Err()
		}
		transcript.Unlock()
		_ = reader.Close()
	}()
	Expect(command.Start()).To(Succeed())
	go func() { child.waitErr = command.Wait(); _ = writer.Close(); close(child.exited) }()
	DeferCleanup(func() { child.stop() })
	child.awaitFrames(1)
	frames, _, err := transcript.snapshot()
	Expect(err).NotTo(HaveOccurred())
	Expect(frames[0].Version).To(Equal(1))
	Expect(frames[0].Type).To(Equal("ready"))
	return child
}

func (child *manualChild) send(value any) {
	Expect(json.NewEncoder(child.stdin).Encode(value)).To(Succeed())
}

func (child *manualChild) watch(request, subscription, path string, accepted bool) {
	before, _, err := child.transcript.snapshot()
	Expect(err).NotTo(HaveOccurred())
	child.send(map[string]any{"version": 1, "type": "watch", "request_id": request, "subscription_id": subscription, "watch_args": map[string]string{"trigger_file": path}})
	child.awaitFrames(len(before) + 1)
	frames, _, err := child.transcript.snapshot()
	Expect(err).NotTo(HaveOccurred())
	frame := frames[len(before)]
	Expect(frame.Type).To(Equal("watch_result"))
	Expect(frame.Version).To(Equal(1))
	Expect(frame.RequestID).To(Equal(request))
	Expect(frame.Accepted).NotTo(BeNil())
	Expect(*frame.Accepted).To(Equal(accepted))
	if !accepted {
		Expect(frame.Error).NotTo(BeEmpty())
	}
}

func (child *manualChild) awaitFrames(count int) {
	Eventually(func() int { frames, _, _ := child.transcript.snapshot(); return len(frames) }, time.Until(child.deadline), 5*time.Millisecond).Should(BeNumerically(">=", count))
}

func (child *manualChild) stop() {
	if child.stopped {
		return
	}
	child.stopped = true
	cleanupStarted := time.Now()
	defer func() { AddReportEntry("confirmed process cleanup seconds", time.Since(cleanupStarted).Seconds()) }()
	select {
	case <-child.exited:
	default:
		_ = json.NewEncoder(child.stdin).Encode(map[string]any{"version": 1, "type": "shutdown"})
		_ = child.stdin.Close()
		select {
		case <-child.exited:
		case <-time.After(max(time.Until(child.deadline), time.Millisecond)):
			_ = child.command.Process.Kill()
			select {
			case <-child.exited:
			case <-time.After(time.Second):
				Fail("owned manual plugin termination was not confirmed")
			}
			Fail("manual plugin missed normal shutdown deadline")
		}
	}
	<-child.readDone
	Expect(child.waitErr).NotTo(HaveOccurred())
	_, _, err := child.transcript.snapshot()
	Expect(err).NotTo(HaveOccurred())
}

func publishManualFile(path string, data []byte) {
	temporary := path + ".publish"
	Expect(os.WriteFile(temporary, data, 0o600)).To(Succeed())
	Expect(os.Rename(temporary, path)).To(Succeed())
}

func manualCaseDirectory() (string, time.Time) {
	started := time.Now()
	AddReportEntry("case started UTC", started.UTC().Format(time.RFC3339Nano))
	directory, err := os.MkdirTemp("", "aph-manual-case-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(os.RemoveAll(directory)).To(Succeed())
		AddReportEntry("case finished UTC", time.Now().UTC().Format(time.RFC3339Nano))
		AddReportEntry("case elapsed seconds", time.Since(started).Seconds())
		AddReportEntry("owned process peak", 1)
		Expect(time.Since(started)).To(BeNumerically("<=", 5*time.Second))
	})
	return directory, started.Add(5 * time.Second)
}

var _ = Describe("MVP manual plugin public protocol", func() {
	It("preserves the context bound and rejects representative invalid files", Label("case:MVP-V04-MANUAL-INVALID-CONTENT"), func() {
		directory, deadline := manualCaseDirectory()
		path := filepath.Join(directory, "next.txt")
		child := startManualChild(deadline)
		child.watch("valid-watch", "accepted-subscription", path, true)
		valid := strings.Repeat("é", 4096)
		publishManualFile(path, []byte(valid))
		child.awaitFrames(3)
		frames, _, err := child.transcript.snapshot()
		Expect(err).NotTo(HaveOccurred())
		Expect(frames[2].Type).To(Equal("event"))
		Expect(frames[2].Context).To(Equal(valid))
		Expect(frames[2].SubscriptionID).To(Equal("accepted-subscription"))
		_, err = os.Stat(path)
		Expect(os.IsNotExist(err)).To(BeTrue())
		claimed, err := filepath.Glob(filepath.Join(directory, ".agent-pulse-hub-claim-*"))
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(BeEmpty())
		for index, data := range [][]byte{[]byte(valid + "a"), {0xff, 0xfe}} {
			publishManualFile(path, data)
			Eventually(func() int {
				_, diagnostics, _ := child.transcript.snapshot()
				return strings.Count(diagnostics, "claimed trigger failed at ")
			}, time.Until(deadline), 5*time.Millisecond).Should(Equal(index + 1))
			_, err = os.Stat(path)
			Expect(os.IsNotExist(err)).To(BeTrue())
			claimed, err = filepath.Glob(filepath.Join(directory, ".agent-pulse-hub-claim-*"))
			Expect(err).NotTo(HaveOccurred())
			Expect(claimed).To(HaveLen(index + 1))
			for _, claim := range claimed {
				_, diagnostics, _ := child.transcript.snapshot()
				Expect(diagnostics).To(ContainSubstring(claim))
			}
		}
		child.stop()
		frames, diagnostics, err := child.transcript.snapshot()
		Expect(err).NotTo(HaveOccurred())
		Expect(frames).To(HaveLen(3))
		Expect(strings.Count(diagnostics, "claimed trigger failed at ")).To(Equal(2))
		for _, line := range strings.Split(strings.TrimSpace(diagnostics), "\n") {
			Expect(len(strings.TrimSuffix(line, "\r"))).To(BeNumerically("<=", 1024))
		}
		contents := map[string]bool{}
		for _, claim := range claimed {
			data, err := os.ReadFile(claim)
			Expect(err).NotTo(HaveOccurred())
			contents[string(data)] = true
		}
		Expect(contents).To(HaveKey(valid + "a"))
		Expect(contents).To(HaveKey(string([]byte{0xff, 0xfe})))
	})

	It("rejects an equivalent watched path and an existing trigger", Label("case:MVP-V02-MANUAL-PATHS"), func() {
		directory, deadline := manualCaseDirectory()
		path := filepath.Join(directory, "next.txt")
		existing := filepath.Join(directory, "existing.txt")
		Expect(os.WriteFile(existing, []byte("keep-existing"), 0o600)).To(Succeed())
		child := startManualChild(deadline)
		child.watch("original", "accepted-subscription", path, true)
		equivalent := strings.ReplaceAll(directory, "\\", "/") + "/./next.txt"
		child.watch("equivalent", "rejected-equivalent", equivalent, false)
		child.watch("existing", "rejected-existing", existing, false)
		publishManualFile(path, []byte("manual-path-marker"))
		child.awaitFrames(5)
		child.stop()
		frames, diagnostics, err := child.transcript.snapshot()
		Expect(err).NotTo(HaveOccurred())
		Expect(diagnostics).To(BeEmpty())
		Expect(frames).To(HaveLen(5))
		Expect(frames[4].Type).To(Equal("event"))
		Expect(frames[4].SubscriptionID).To(Equal("accepted-subscription"))
		Expect(frames[4].Context).To(Equal("manual-path-marker"))
		data, err := os.ReadFile(existing)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("keep-existing"))
		_, err = os.Stat(path)
		Expect(os.IsNotExist(err)).To(BeTrue())
		claimed, err := filepath.Glob(filepath.Join(directory, ".agent-pulse-hub-claim-*"))
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(BeEmpty())
	})
})
