//go:build integration

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	. "github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc"
)

var (
	gpwSuite                *mvpCaseEnv
	gpwPlugin, gpwGH, gpwCA string
	gpwCertificate          tls.Certificate
	gpwDaemonEnvironment    map[string]string
)

var _ = BeforeSuite(func() {
	t := GinkgoT()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := os.MkdirTemp("", "gpw-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	gpwSuite = &mvpCaseEnv{root: root, workspace: workspace}
	gpwPlugin = filepath.Join(workspace, "pr-watch"+exeSuffix())
	gpwSuite.hub = filepath.Join(workspace, "hub"+exeSuffix())
	gpwSuite.model = filepath.Join(workspace, "model"+exeSuffix())
	gpwGH = filepath.Join(workspace, "gh"+exeSuffix())
	for _, item := range []struct{ args []string }{{[]string{"test", "-c", "-tags=integration", "-o", gpwPlugin, "./plugins/triggers/github-pr-watch"}}, {[]string{"build", "-o", gpwSuite.hub, "./cmd/agent-pulse-hub"}}, {[]string{"build", "-o", gpwSuite.model, "./plugins/actions/model-output"}}, {[]string{"build", "-o", gpwGH, "./cmd/github-pr-watch-verification/testdata/gh.go"}}} {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		cmd := exec.CommandContext(ctx, "go", item.args...)
		cmd.Dir = root
		started := time.Now()
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build %v: %v\n%s", item.args, err, output)
		}
		t.Logf("build %v: %s", item.args, time.Since(started))
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(79), Subject: pkix.Name{CommonName: "PR watch acceptance CA"}, DNSNames: []string{"api.github.com", "ghes.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	gpwCertificate = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	gpwCA = filepath.Join(workspace, "ca.pem")
	if err := os.WriteFile(gpwCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
})

var _ = AfterSuite(func() {
	if gpwSuite != nil {
		gpwSuite.cleanup()
	}
})

func gpwEnvironment(overrides map[string]string) []string {
	result := []string{}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		replace := false
		for candidate := range overrides {
			if strings.EqualFold(key, candidate) {
				replace = true
				break
			}
		}
		if !replace {
			result = append(result, entry)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

type (
	gpwRequest struct{ Host, Path, Token, Query, Cursor string }
	gpwFixture struct {
		signal           signalFixtureState
		snapshot         signalFixtureState
		signalReads      int
		mu               sync.Mutex
		server           *httptest.Server
		revision         int
		mode             string
		requests         []gpwRequest
		rounds           int
		valid            map[string]bool
		large            bool
		prelude          bool
		extra            int
		observedRevision int
		observedMode     string
		observedLarge    bool
		observedExtra    int
		edit             int
		observedEdit     int
		capacity         int
		observedCapacity int
	}
)

func newGPWFixture(t mvpReporter) *gpwFixture {
	f := &gpwFixture{mode: "complete", valid: map[string]bool{"high": true, "low": true, "enterprise": true, "fixture-saved-token": true}}
	f.signal = signalFixtureState{CI: 1, Base: 1, Conflict: "MERGEABLE", Draft: true}
	server := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{gpwCertificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	f.server = server
	DeferCleanup(server.Close)
	return f
}

func (f *gpwFixture) environment(directory string) map[string]string {
	return map[string]string{"APH_GPW_PLUGIN_MODE": "1", "APH_GPW_TEST_ROOT_CERT": gpwCA, "APH_GPW_TEST_SERVER_ADDRESS": strings.TrimPrefix(f.server.URL, "https://"), "APH_GPW_PROTOCOL_JOURNAL": filepath.Join(directory, "protocol.jsonl"), "GH_TOKEN": "high", "GITHUB_TOKEN": "low", "PATH": directory}
}

func (f *gpwFixture) set(revision int, mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revision = revision
	f.mode = mode
}
func (f *gpwFixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.rounds }
func (f *gpwFixture) calls() []gpwRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gpwRequest(nil), f.requests...)
}

func (f *gpwFixture) waitRounds(t mvpReporter, count int) {
	if !waitMVP(t, 3*time.Second, func() bool { return f.count() >= count }) {
		t.Fatalf("source did not complete %d rounds; calls=%+v", count, f.calls())
	}
}

func gpwConnection(nodes []any, more bool, cursor string) map[string]any {
	return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor}}
}

func gpwComment(id, body string) map[string]any {
	return map[string]any{"id": id, "body": body, "author": map[string]any{"login": "author"}, "createdAt": "2026-10-27T00:00:00Z", "updatedAt": "2026-10-27T00:00:00Z", "url": "https://github.com/acme/widget/pull/17#" + id}
}

type gpwHost struct {
	client    *plugin.Client
	stream    pluginprotocol.SessionClient
	cancel    context.CancelFunc
	frames    chan map[string]any
	log       string
	file      *os.File
	streamed  bool
	closeOnce sync.Once
}

func startGPWHost(t mvpReporter, directory, host string, f *gpwFixture, overrides map[string]string) *gpwHost {
	env := f.environment(directory)
	for k, v := range overrides {
		env[k] = v
	}
	command := exec.Command(gpwPlugin, "--host", host, "--poll-interval", "5")
	command.Env = gpwEnvironment(env)
	logPath := filepath.Join(directory, fmt.Sprintf("plugin-%d.log", time.Now().UnixNano()))
	file, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	client := plugin.NewClient(&plugin.ClientConfig{HandshakeConfig: pluginprocess.HandshakeConfig, Plugins: plugin.PluginSet{"input": &pluginprocess.FrameworkPlugin{}}, Cmd: command, AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, StartTimeout: 3 * time.Second, Logger: hclog.NewNullLogger(), Stderr: file, SkipHostEnv: true})
	rpc, err := client.Client()
	if err != nil {
		client.Kill()
		t.Fatalf("plugin launch: %v log=%s", err, readMVPFile(logPath))
	}
	value, err := rpc.Dispense("input")
	if err != nil {
		client.Kill()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// FrameworkPlugin exposes the real gRPC connection.
	stream, err := pluginprotocol.OpenSession(ctx, value.(grpc.ClientConnInterface))
	if err != nil {
		cancel()
		client.Kill()
		t.Fatal(err)
	}
	h := &gpwHost{client: client, stream: stream, cancel: cancel, frames: make(chan map[string]any, 64), log: logPath, file: file}
	go func() {
		for {
			payload, err := stream.Recv()
			if err != nil {
				return
			}
			var frame map[string]any
			if len(payload) > 65536 {
				return
			}
			if json.Unmarshal(payload, &frame) != nil {
				return
			}
			select {
			case h.frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	ready := h.receive(t, "ready", "")
	caps, _ := json.Marshal(ready["capabilities"])
	if !strings.Contains(string(caps), "rewait_v1") || !strings.Contains(string(caps), "runtime_state_v2") {
		t.Fatalf("missing capabilities: %s", caps)
	}
	h.streamed = strings.Contains(string(caps), "runtime_state_stream_v1")
	if !h.streamed {
		t.Fatal("missing streamed-state capability")
	}
	DeferCleanup(h.close)
	return h
}

func (h *gpwHost) close() { h.closeOnce.Do(func() { h.cancel(); h.client.Kill(); _ = h.file.Close() }) }

func (h *gpwHost) send(t mvpReporter, frame map[string]any) {
	frame["version"] = 2
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 65536 {
		t.Fatalf("oversized host frame %d", len(data))
	}
	if err := h.stream.Send(data); err != nil {
		t.Fatal(err)
	}
}

func (h *gpwHost) receive(t mvpReporter, kind, id string) map[string]any {
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case frame := <-h.frames:
			if frame["type"] == "state_event" && kind != "state_event" {
				t.Fatalf("unexpected event before %s: %+v", kind, frame)
			}
			if frame["type"] == kind && (id == "" || frame["request_id"] == id) {
				return frame
			}
		case <-timer.C:
			t.Fatalf("missing %s/%s log=%s", kind, id, readMVPFile(h.log))
			return nil
		}
	}
}

func (h *gpwHost) watch(t mvpReporter, id, host string) map[string]any {
	return h.restore(t, id, host, "", nil)
}

func (h *gpwHost) restore(t mvpReporter, id, host, schema string, chunks []any) map[string]any {
	request := "watch-" + id
	var wireSchema any
	if schema != "" {
		wireSchema = schema
	}
	h.send(t, map[string]any{"type": "state_stream_watch", "request_id": request, "subscription_id": id, "watch_args": gpwArgs(host), "schema": wireSchema, "chunk_count": strconv.Itoa(len(chunks))})
	for i, chunk := range chunks {
		h.send(t, map[string]any{"type": "state_stream_chunk", "request_id": request, "subscription_id": id, "index": strconv.Itoa(i), "data": chunk})
	}
	h.send(t, map[string]any{"type": "state_stream_end", "request_id": request, "subscription_id": id, "chunk_count": strconv.Itoa(len(chunks))})
	return h.receive(t, "watch_result", request)
}

func (h *gpwHost) checkpoint(t mvpReporter, id, request string) map[string]any {
	h.send(t, map[string]any{"type": "checkpoint", "request_id": request, "subscription_id": id, "quiesce": false})
	begin := h.receive(t, "checkpoint_stream_begin", request)
	chunks := []any{}
	for {
		frame := h.nextFrame(t)
		if frame["request_id"] != request || frame["subscription_id"] != id {
			t.Fatalf("foreign checkpoint frame %v", frame)
		}
		if frame["type"] == "checkpoint_stream_end" {
			if frame["chunk_count"] != strconv.Itoa(len(chunks)) || len(chunks) == 0 {
				t.Fatalf("bad end %v", frame)
			}
			break
		}
		if frame["type"] != "checkpoint_stream_chunk" || frame["index"] != strconv.Itoa(len(chunks)) {
			t.Fatalf("bad chunk %v", frame)
		}
		data := frame["data"]
		raw, _ := json.Marshal(data)
		if len(raw) > 32768 {
			t.Fatalf("oversized chunk %d", len(raw))
		}
		chunks = append(chunks, data)
	}
	return map[string]any{"accepted": true, "schema": begin["schema"], "chunks": chunks}
}

func (h *gpwHost) nextFrame(t mvpReporter) map[string]any {
	select {
	case frame := <-h.frames:
		return frame
	case <-time.After(3 * time.Second):
		t.Fatalf("missing stream frame: %s", readMVPFile(h.log))
		return nil
	}
}

func (h *gpwHost) noEvent(t mvpReporter) {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case frame := <-h.frames:
		if frame["type"] == "state_event" {
			t.Fatalf("unexpected event: %+v", frame)
		}
	case <-timer.C:
	}
}

func gpwArgs(host string) map[string]any {
	return map[string]any{"host": host, "owner": "acme", "repository": "widget", "pull_number": 17, "waiting_reason": "wait for the other participant", "represented_actor": "author or reviewer"}
}
