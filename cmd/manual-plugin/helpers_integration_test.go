//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
)

var manualExecutable string

var _ = BeforeSuite(func(ctx SpecContext) {
	directory, err := os.MkdirTemp("", "manual-plugin-suite-")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(os.RemoveAll(directory)).To(Succeed()) })
	// An explicit output name works on every supported OS, including Windows.
	manualExecutable = filepath.Join(directory, "manual-plugin.exe")
	command := exec.CommandContext(ctx, "go", "build", "-o", manualExecutable, ".")
	output, err := command.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "build manual-plugin: %s", output)
}, NodeTimeout(90*time.Second))

// This host exercises the published Session directly on go-plugin's connection.
// The child uses the real command, manual adapter and filesystem operations.
type manualHostPlugin struct{ plugin.NetRPCUnsupportedPlugin }

func (manualHostPlugin) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return errors.New("the acceptance host is not a plugin server")
}

func (manualHostPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, connection *grpc.ClientConn) (any, error) {
	return connection, nil
}

type manualFrameResult struct {
	frame pluginprotocol.Frame
	err   error
}

type manualHost struct {
	stream      pluginprotocol.SessionClient
	frames      chan manualFrameResult
	diagnostics *manualDiagnosticBuffer
	receiver    chan struct{}
}

func startManualHost(ctx context.Context) *manualHost {
	started := time.Now()
	sessionCtx, cancel := context.WithCancel(ctx)
	buffer := &manualDiagnosticBuffer{}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig:  plugin.HandshakeConfig{ProtocolVersion: 1, MagicCookieKey: "AGENT_PULSE_HUB_PLUGIN", MagicCookieValue: "plugin-v1"},
		Plugins:          plugin.PluginSet{"events": manualHostPlugin{}},
		Cmd:              exec.Command(manualExecutable),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout:     2 * time.Second,
		Logger:           hclog.NewNullLogger(),
		Stderr:           buffer,
	})
	host := &manualHost{frames: make(chan manualFrameResult, 32), diagnostics: buffer, receiver: make(chan struct{})}
	DeferCleanup(func() {
		cancel()
		if host.stream != nil {
			_ = host.stream.CloseSend()
		}
		client.Kill()
		Expect(client.Exited()).To(BeTrue(), "configured plugin did not exit")
		if host.stream != nil {
			select {
			case <-host.receiver:
			case <-time.After(time.Second):
				Fail("Session receiver did not terminate after plugin cleanup")
			}
		}
		Expect(time.Since(started)).To(BeNumerically("<=", 5*time.Second), "case runtime including plugin cleanup")
	})
	protocol, err := client.Client()
	Expect(err).NotTo(HaveOccurred(), "start manual-plugin: %s", buffer.String())
	Expect(client.Protocol()).To(Equal(plugin.ProtocolGRPC))
	raw, err := protocol.Dispense("events")
	Expect(err).NotTo(HaveOccurred())
	connection, ok := raw.(*grpc.ClientConn)
	Expect(ok).To(BeTrue())
	host.stream, err = pluginprotocol.OpenSession(sessionCtx, connection)
	Expect(err).NotTo(HaveOccurred())
	go func() {
		defer close(host.receiver)
		for {
			payload, err := host.stream.Recv()
			var frame pluginprotocol.Frame
			if err == nil {
				frame, err = pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
			}
			select {
			case host.frames <- manualFrameResult{frame: frame, err: err}:
			case <-sessionCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	Expect(host.next(ctx).Type).To(Equal("ready"))
	return host
}

func (h *manualHost) next(ctx context.Context) pluginprotocol.Frame {
	select {
	case result := <-h.frames:
		Expect(result.err).NotTo(HaveOccurred(), "Session receive: %s", h.diagnostics.String())
		return result.frame
	case <-ctx.Done():
		Fail("Session frame deadline expired: " + h.diagnostics.String())
		return pluginprotocol.Frame{}
	}
}

func (h *manualHost) watch(ctx context.Context, id, path string, accepted bool) {
	payload, err := pluginprotocol.Encode(pluginprotocol.Frame{
		Type: "watch", RequestID: id, SubscriptionID: id,
		WatchArgs: jsonvalue.NewObject(map[string]*jsonvalue.Value{"trigger_file": jsonvalue.NewString(path)}),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(h.stream.Send(payload)).To(Succeed())
	result := h.next(ctx)
	Expect(result.Type).To(Equal("watch_result"))
	Expect(result.RequestID).To(Equal(id))
	Expect(result.Accepted).NotTo(BeNil())
	Expect(*result.Accepted).To(Equal(accepted))
	if !accepted {
		Expect(result.Error).NotTo(BeEmpty())
	}
}

func (h *manualHost) event(ctx context.Context, subscription, content string) {
	frame := h.next(ctx)
	Expect(frame.Type).To(Equal("event"))
	Expect(frame.SubscriptionID).To(Equal(subscription))
	Expect(frame.Context).To(Equal(content))
}

func (h *manualHost) noFrames(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case result := <-h.frames:
		Fail("unexpected Session frame or failure: " + result.frame.Type + " " + h.diagnostics.String())
	case <-ctx.Done():
		Fail("Session observation deadline expired: " + h.diagnostics.String())
	case <-timer.C:
	}
}

func (h *manualHost) waitRejections(ctx context.Context, count int) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for strings.Count(h.diagnostics.String(), "manual_plugin_trigger_rejected") < count {
		select {
		case result := <-h.frames:
			Fail("invalid trigger produced a Session frame or failure: " + result.frame.Type)
		case <-ctx.Done():
			Fail("trigger diagnostic deadline expired: " + h.diagnostics.String())
		case <-ticker.C:
		}
	}
}

func publishManualTrigger(path string, content []byte) {
	file, err := os.CreateTemp(filepath.Dir(path), "publish-*")
	Expect(err).NotTo(HaveOccurred())
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	Expect(writeErr).NotTo(HaveOccurred())
	Expect(closeErr).NotTo(HaveOccurred())
	_, err = os.Lstat(path)
	Expect(os.IsNotExist(err)).To(BeTrue(), "publisher requires an absent trigger")
	Expect(os.Rename(temporary, path)).To(Succeed())
}

func manualClaims(directory string) []string {
	claims, err := filepath.Glob(filepath.Join(directory, ".agent-pulse-hub-claim-*"))
	Expect(err).NotTo(HaveOccurred())
	return claims
}

type manualDiagnosticBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *manualDiagnosticBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *manualDiagnosticBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
