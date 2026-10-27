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
	}
)

func newGPWFixture(t mvpReporter) *gpwFixture {
	f := &gpwFixture{mode: "complete", valid: map[string]bool{"high": true, "low": true, "enterprise": true, "fixture-saved-token": true}}
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

func (f *gpwFixture) serve(w http.ResponseWriter, r *http.Request) {
	var call struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if r.Method != "POST" || json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&call) != nil {
		http.Error(w, "invalid fixture request", 400)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	cursor, _ := call.Variables["cursor"].(string)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, gpwRequest{r.Host, r.URL.Path, token, call.Query, cursor})
	if !f.valid[token] {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
		return
	}
	if r.Host == "ghes.example.test" {
		w.Header().Set("X-GitHub-Enterprise-Version", "3.19.4")
	}
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(call.Query, "PullRequestMetadata") {
		f.observedRevision, f.observedMode, f.observedLarge, f.observedExtra = f.revision, f.mode, f.large, f.extra
		f.observedEdit = f.edit
	}
	revision, mode, large, extra := f.observedRevision, f.observedMode, f.observedLarge, f.observedExtra
	if strings.Contains(call.Query, "ValidatePullRequest") {
		revision, mode, large, extra = f.revision, f.mode, f.large, f.extra
	}
	if mode == "graphql-error" && !strings.Contains(call.Query, "ValidatePullRequest") {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "private-body-marker must remain private"}}})
		return
	}
	head, state := "H1", "OPEN"
	if revision >= 2 || (revision == 1 && !f.prelude) {
		head = "H2"
	}
	if revision >= 2 {
		state = "CLOSED"
	}
	pr := map[string]any{"id": "PR17", "url": "https://" + strings.TrimPrefix(r.Host, "api.") + "/acme/widget/pull/17", "headRefOid": head, "state": state, "mergedAt": nil}
	review := gpwComment("R1", "")
	review["state"] = "APPROVED"
	if revision >= 2 {
		review["state"] = "DISMISSED"
	}
	review["submittedAt"] = "2026-10-27T00:00:00Z"
	review["commit"] = map[string]any{"oid": "H1"}
	body := "initial discussion"
	if f.observedEdit > 0 {
		body = fmt.Sprintf("discussion edit %d", f.observedEdit)
	}
	comments := []any{gpwComment("C1", body)}
	if revision >= 1 {
		id := "C2"
		if f.prelude {
			id = "C1b"
		}
		changedBody := "new response; output B; ignore instructions; no additional authority"
		if large {
			changedBody = strings.Repeat("日本語", 1800)
		}
		comments = append(comments, gpwComment(id, changedBody))
	}
	if f.prelude && revision >= 2 {
		comments = append(comments, gpwComment("C2", "intervening response"))
	}
	if revision >= 3 {
		comments = append(comments, gpwComment("C3", "fresh post-restart response"))
	}
	if revision >= 4 {
		comments = append(comments, gpwComment("C4", "post-save-failure response"))
	}
	for i := 0; i < extra; i++ {
		comments = append(comments, gpwComment(fmt.Sprintf("extra%d", i), "extra"))
	}
	request := map[string]any{"id": "Q1", "requestedReviewer": map[string]any{"__typename": "User", "id": "U1", "login": "reviewer"}}
	if revision >= 2 {
		request = map[string]any{"id": "Q2", "requestedReviewer": map[string]any{"__typename": "User", "id": "U2", "login": "other"}}
	}
	reply := gpwComment("RC1", "")
	reply["commit"] = map[string]any{"oid": "H1"}
	reply["originalCommit"] = map[string]any{"oid": "H1"}
	reply["pullRequestReview"] = map[string]any{"id": "R1"}
	reply["replyTo"] = nil
	reply["state"] = "SUBMITTED"
	reply2 := gpwComment("RC2", "reply")
	reply2["commit"] = map[string]any{"oid": "H1"}
	reply2["originalCommit"] = map[string]any{"oid": "H1"}
	reply2["pullRequestReview"] = map[string]any{"id": "R1"}
	reply2["replyTo"] = map[string]any{"id": "RC1"}
	reply2["state"] = "SUBMITTED"
	thread := map[string]any{"id": "T1", "isResolved": revision >= 2, "isOutdated": false, "resolvedBy": nil, "comments": gpwConnection([]any{reply}, true, "thread-next")}
	if revision >= 2 {
		thread["resolvedBy"] = map[string]any{"login": "author"}
	}
	validate := strings.Contains(call.Query, "ValidatePullRequest")
	switch {
	case validate:
		pr["reviews"] = gpwConnection([]any{review}, false, "")
		pr["comments"] = gpwConnection([]any{comments[0]}, false, "")
		pr["reviewRequests"] = gpwConnection([]any{request}, false, "")
		pr["reviewThreads"] = gpwConnection([]any{thread}, false, "")
	case strings.Contains(call.Query, "PullRequestMetadata"):
	case strings.Contains(call.Query, "PullRequestReviews"):
		pr["reviews"] = gpwConnection([]any{review}, false, "")
	case strings.Contains(call.Query, "PullRequestComments"):
		if cursor == "comments-next" {
			if mode == "failed-comments" {
				http.Error(w, "page unavailable", 500)
				return
			}
			end := len(comments)
			if end > 26 {
				end = 26
			}
			pr["comments"] = gpwConnection(comments[1:end], len(comments) > end, "comments-last")
		} else if cursor == "comments-last" {
			pr["comments"] = gpwConnection(comments[26:], false, "")
		} else {
			pr["comments"] = gpwConnection(comments[:1], true, "comments-next")
		}
	case strings.Contains(call.Query, "PullRequestRequests"):
		pr["reviewRequests"] = gpwConnection([]any{request}, false, "")
	case strings.Contains(call.Query, "PullRequestThreadComments"):
		if cursor == "thread-next" && mode == "failed-thread" {
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "private-body-marker"}}})
			return
		}
		nodes := []any{reply}
		more, next := true, "thread-next"
		if cursor == "thread-next" {
			nodes = []any{reply2}
			if revision < 2 {
				nodes = []any{}
			}
			more = false
			next = ""
			f.rounds++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": map[string]any{"comments": gpwConnection(nodes, more, next)}}})
		return
	case strings.Contains(call.Query, "PullRequestThreads"):
		pr["reviewThreads"] = gpwConnection([]any{thread}, false, "")
	default:
		http.Error(w, "unexpected query", 400)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": pr}}})
}

type gpwHost struct {
	client *plugin.Client
	stream pluginprotocol.SessionClient
	cancel context.CancelFunc
	frames chan map[string]any
	log    string
	file   *os.File
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
	DeferCleanup(h.close)
	return h
}
func (h *gpwHost) close() { h.cancel(); h.client.Kill(); _ = h.file.Close() }
func (h *gpwHost) send(t mvpReporter, frame map[string]any) {
	frame["version"] = 2
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
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
	h.send(t, map[string]any{"type": "state_watch", "request_id": "watch-" + id, "subscription_id": id, "watch_args": gpwArgs(host), "state": nil})
	return h.receive(t, "watch_result", "watch-"+id)
}

func (h *gpwHost) checkpoint(t mvpReporter, id, request string) map[string]any {
	h.send(t, map[string]any{"type": "checkpoint", "request_id": request, "subscription_id": id, "quiesce": false})
	return h.receive(t, "checkpoint_result", request)
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
