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
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprocess"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	. "github.com/onsi/ginkgo/v2"
	"google.golang.org/grpc"
)

type grrReporter interface {
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
	Errorf(string, ...any)
	Logf(string, ...any)
}

type grrReceive struct {
	frame pluginprotocol.Frame
	err   error
}

type grrPluginHost struct {
	client  *plugin.Client
	cmd     *exec.Cmd
	stream  pluginprotocol.SessionClient
	frames  chan grrReceive
	ctx     context.Context
	cancel  context.CancelFunc
	logPath string
	logFile *os.File
}

type grrItem struct {
	ID  uint64
	URL string
}

type grrRequest struct {
	Sequence   uint64
	Host       string
	Path       string
	Query      string
	Token      string
	Accept     string
	APIVersion string
	At         time.Time
	Mode       string
	Release    chan struct{}
	Answered   chan time.Time
}

type grrFixture struct {
	mu          sync.Mutex
	server      *httptest.Server
	validTokens map[string]uint64
	items       []grrItem
	mode        string
	retryAfter  string
	holdSearch  bool
	searchCount uint64
	authCalls   []grrRequest
	searchCalls chan grrRequest
	holdAuthAt  int
	authArrived chan struct{}
	authRelease chan struct{}
}

var (
	grrSuiteDirectory  string
	grrPluginBinary    string
	grrHubBinary       string
	grrModelBinary     string
	grrGHBInary        string
	grrRootCertificate string
	grrTLSCertificate  tls.Certificate
)

var _ = BeforeSuite(func() {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		Fail(fmt.Sprintf("resolve repository root: %v", err))
	}
	grrSuiteDirectory, err = os.MkdirTemp("", "github-review-request-suite-")
	if err != nil {
		Fail(fmt.Sprintf("create suite directory: %v", err))
	}
	grrRootCertificate = filepath.Join(grrSuiteDirectory, "fixture-ca.pem")
	grrTLSCertificate, err = createGRRCertificate()
	if err != nil {
		Fail(fmt.Sprintf("create test TLS certificate: %v", err))
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: grrTLSCertificate.Certificate[0]})
	if err := os.WriteFile(grrRootCertificate, certificatePEM, 0o600); err != nil {
		Fail(fmt.Sprintf("write test CA certificate: %v", err))
	}

	goExe, err := exec.Command("go", "env", "GOEXE").Output()
	if err != nil {
		Fail(fmt.Sprintf("read executable suffix: %v", err))
	}
	suffix := strings.TrimSpace(string(goExe))
	grrPluginBinary = filepath.Join(grrSuiteDirectory, "github-review-request-test"+suffix)
	grrHubBinary = filepath.Join(grrSuiteDirectory, "agent-pulse-hub"+suffix)
	grrModelBinary = filepath.Join(grrSuiteDirectory, "model-output"+suffix)
	grrGHBInary = filepath.Join(grrSuiteDirectory, "gh-fixture"+suffix)
	builds := []struct {
		label string
		args  []string
		dir   string
	}{
		{label: "integration plugin", args: []string{"test", "-c", "-tags=integration", "-o", grrPluginBinary, "./plugins/triggers/github-review-request"}, dir: root},
		{label: "hub", args: []string{"build", "-o", grrHubBinary, "./cmd/agent-pulse-hub"}, dir: root},
		{label: "model-output action", args: []string{"build", "-o", grrModelBinary, "./plugins/actions/model-output"}, dir: root},
		{label: "gh process fixture", args: []string{"build", "-o", grrGHBInary, "./plugins/triggers/github-review-request/testdata/gh.go"}, dir: root},
	}
	for _, build := range builds {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		command := exec.CommandContext(ctx, "go", build.args...)
		command.Dir = build.dir
		started := time.Now()
		output, buildErr := command.CombinedOutput()
		cancel()
		if buildErr != nil {
			Fail(fmt.Sprintf("build %s after %s: %v\n%s", build.label, time.Since(started), buildErr, output))
		}
		GinkgoT().Logf("built %s in %s", build.label, time.Since(started))
	}
})

var _ = AfterSuite(func() {
	if grrSuiteDirectory != "" {
		_ = os.RemoveAll(grrSuiteDirectory)
	}
})

func createGRRCertificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(73), Subject: pkix.Name{CommonName: "GitHub review request test CA"},
		DNSNames:  []string{"api.github.com", "ghes.example.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(3 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func newGRRFixture(t grrReporter, validTokens map[string]uint64) *grrFixture {
	t.Helper()
	fixture := &grrFixture{
		validTokens: make(map[string]uint64, len(validTokens)),
		mode:        "complete", retryAfter: "1", searchCalls: make(chan grrRequest, 512),
	}
	for token, id := range validTokens {
		fixture.validTokens[token] = id
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(fixture.serveHTTP))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{grrTLSCertificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	fixture.server = server
	DeferCleanup(server.Close)
	return fixture
}

func (f *grrFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.URL.Path == "/user" || r.URL.Path == "/api/v3/user" {
		f.mu.Lock()
		id, accepted := f.validTokens[token]
		f.authCalls = append(f.authCalls, grrRequest{Host: r.Host, Path: r.URL.Path, Token: token, Accept: r.Header.Get("Accept"), APIVersion: r.Header.Get("X-GitHub-Api-Version"), At: time.Now()})
		hold := f.holdAuthAt != 0 && len(f.authCalls) == f.holdAuthAt
		arrived, release := f.authArrived, f.authRelease
		f.mu.Unlock()
		if hold {
			close(arrived)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if !accepted {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%d,"login":"user-%d"}`, id, id)
		return
	}
	if r.URL.Path != "/search/issues" && r.URL.Path != "/api/v3/search/issues" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	f.mu.Lock()
	id, accepted := f.validTokens[token]
	f.searchCount++
	request := grrRequest{
		Sequence: f.searchCount, Host: r.Host, Path: r.URL.Path, Query: r.URL.RawQuery,
		Token: token, Accept: r.Header.Get("Accept"), APIVersion: r.Header.Get("X-GitHub-Api-Version"),
		At: time.Now(), Mode: f.mode,
	}
	items, mode, retryAfter, hold := append([]grrItem(nil), f.items...), f.mode, f.retryAfter, f.holdSearch
	if hold {
		request.Release = make(chan struct{})
		request.Answered = make(chan time.Time, 1)
	}
	f.mu.Unlock()
	select {
	case f.searchCalls <- request:
	default:
	}
	if request.Release != nil {
		select {
		case <-request.Release:
		case <-r.Context().Done():
			return
		}
		request.Answered <- time.Now()
	}
	if !accepted {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
		return
	}
	if mode == "rate-limit" {
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if mode == "incomplete" {
		_, _ = fmt.Fprint(w, grrSearchBody(items, true))
	} else {
		_, _ = fmt.Fprint(w, grrSearchBody(items, false))
	}
	_ = id
}

func (f *grrFixture) setSearch(items []grrItem, mode string, retryAfter string) {
	f.mu.Lock()
	f.items = append([]grrItem(nil), items...)
	f.mode = mode
	if retryAfter != "" {
		f.retryAfter = retryAfter
	}
	f.mu.Unlock()
}

func (f *grrFixture) holdSearchResponses() {
	f.mu.Lock()
	f.holdSearch = true
	f.mu.Unlock()
}

func (f *grrFixture) holdSecondRestoredIdentity() (<-chan struct{}, chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authCalls = nil
	f.searchCount = 0
	for len(f.searchCalls) > 0 {
		<-f.searchCalls
	}
	f.holdAuthAt = 2
	f.authArrived = make(chan struct{})
	f.authRelease = make(chan struct{})
	return f.authArrived, f.authRelease
}

func grrReleaseSearch(t grrReporter, request grrRequest) time.Time {
	t.Helper()
	if request.Release == nil {
		return time.Time{}
	}
	close(request.Release)
	select {
	case answered := <-request.Answered:
		return answered
	case <-time.After(2 * time.Second):
		t.Fatalf("search request %d did not complete after release", request.Sequence)
		return time.Time{}
	}
}

func (f *grrFixture) authSnapshot() []grrRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grrRequest(nil), f.authCalls...)
}

func (f *grrFixture) searchCountSnapshot() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searchCount
}

func grrSearchBody(items []grrItem, incomplete bool) string {
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]any{
			"id": item.ID, "html_url": item.URL, "state": "open", "pull_request": map[string]any{},
		})
	}
	body, _ := json.Marshal(map[string]any{"total_count": len(rows), "incomplete_results": incomplete, "items": rows})
	return string(body)
}

func grrEnvironment(overrides map[string]string) []string {
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, replace := lookupEnvOverride(overrides, key); replace {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func lookupEnvOverride(overrides map[string]string, key string) (string, bool) {
	for candidate, value := range overrides {
		if strings.EqualFold(candidate, key) {
			return value, true
		}
	}
	return "", false
}

func grrTestEnvironment(fixture *grrFixture, overrides map[string]string) map[string]string {
	result := map[string]string{
		"APH_GRR_PLUGIN_MODE":         "1",
		"APH_GRR_TEST_ROOT_CERT":      grrRootCertificate,
		"APH_GRR_TEST_SERVER_ADDRESS": strings.TrimPrefix(fixture.server.URL, "https://"),
	}
	for key, value := range overrides {
		result[key] = value
	}
	return result
}

func startGRRPluginHost(t grrReporter, directory string, fixture *grrFixture, overrides map[string]string) *grrPluginHost {
	t.Helper()
	logPath := filepath.Join(directory, fmt.Sprintf("github-review-request-%d.stderr", time.Now().UnixNano()))
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create plugin log: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.Command(grrPluginBinary)
	command.Env = grrEnvironment(grrTestEnvironment(fixture, overrides))
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: pluginprocess.HandshakeConfig,
		Plugins:         plugin.PluginSet{pluginprocess.PluginKey(string(domain.InputRole)): &pluginprocess.FrameworkPlugin{}},
		Cmd:             command, AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		StartTimeout: 5 * time.Second, Logger: hclog.NewNullLogger(), Stderr: logFile, SkipHostEnv: true,
	})
	rpcClient, err := client.Client()
	if err != nil {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatalf("start integration plugin: %v", err)
	}
	value, err := rpcClient.Dispense(pluginprocess.PluginKey(string(domain.InputRole)))
	if err != nil {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatalf("dispense input role: %v", err)
	}
	connection, ok := value.(grpc.ClientConnInterface)
	if !ok {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatal("framework did not return a gRPC connection")
	}
	stream, err := pluginprotocol.OpenSession(ctx, connection)
	if err != nil {
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatalf("open plugin Session: %v", err)
	}
	readyChannel := make(chan grrReceive, 1)
	go func() {
		payload, receiveErr := stream.Recv()
		if receiveErr != nil {
			readyChannel <- grrReceive{err: receiveErr}
			return
		}
		frame, parseErr := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		readyChannel <- grrReceive{frame: frame, err: parseErr}
	}()
	select {
	case result := <-readyChannel:
		if result.err != nil || result.frame.Type != "ready" || !reflect.DeepEqual(result.frame.Capabilities, []string{pluginprotocol.RuntimeStateV2Capability, pluginprotocol.CancelWatchCapability}) {
			cancel()
			client.Kill()
			_ = logFile.Close()
			t.Fatalf("plugin readiness = %+v, error=%v", result.frame, result.err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		client.Kill()
		_ = logFile.Close()
		t.Fatal("plugin readiness timed out")
	}
	host := &grrPluginHost{client: client, cmd: command, stream: stream, frames: make(chan grrReceive, 64), ctx: ctx, cancel: cancel, logPath: logPath, logFile: logFile}
	go host.receive()
	return host
}

func (h *grrPluginHost) receive() {
	for {
		payload, err := h.stream.Recv()
		if err != nil {
			select {
			case h.frames <- grrReceive{err: err}:
			case <-h.ctx.Done():
			}
			return
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.PluginToDaemon)
		select {
		case h.frames <- grrReceive{frame: frame, err: err}:
		case <-h.ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (h *grrPluginHost) send(t grrReporter, frame pluginprotocol.Frame) {
	t.Helper()
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		t.Fatalf("encode daemon frame: %v", err)
	}
	if err := h.stream.Send(payload); err != nil {
		t.Fatalf("send daemon frame: %v", err)
	}
}

func (h *grrPluginHost) watch(t grrReporter, requestID, subscriptionID string, args, state *jsonvalue.Value) pluginprotocol.Frame {
	t.Helper()
	h.send(t, pluginprotocol.Frame{Type: "state_watch", RequestID: requestID, SubscriptionID: subscriptionID, WatchArgs: args, State: state})
	return h.receiveType(t, "watch_result", requestID, 10*time.Second)
}

func (h *grrPluginHost) receiveType(t grrReporter, frameType, requestID string, timeout time.Duration) pluginprotocol.Frame {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case result := <-h.frames:
			if result.err != nil {
				t.Fatalf("receive plugin frame: %v", result.err)
			}
			if result.frame.Type == frameType && (requestID == "" || result.frame.RequestID == requestID) {
				return result.frame
			}
		case <-timer.C:
			t.Fatalf("did not receive %s request_id=%s within %s", frameType, requestID, timeout)
		}
	}
}

func (h *grrPluginHost) event(t grrReporter, timeout time.Duration) pluginprotocol.Frame {
	t.Helper()
	return h.receiveType(t, "state_event", "", timeout)
}

func (h *grrPluginHost) assertNoEvent(t grrReporter, timeout time.Duration) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-h.frames:
		if result.err != nil {
			t.Fatalf("receive plugin frame: %v", result.err)
		}
		if result.frame.Type == "state_event" {
			t.Fatalf("unexpected event: %+v", result.frame)
		}
	case <-timer.C:
	}
}

func (h *grrPluginHost) cleanup() {
	if h == nil {
		return
	}
	_ = h.stream.CloseSend()
	h.cancel()
	done := make(chan struct{})
	go func() { h.client.Kill(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
	}
	_ = h.logFile.Close()
}

func grrWatchArgs(host, interval string) *jsonvalue.Value {
	fields := map[string]*jsonvalue.Value{"poll_interval": jsonvalue.NewString(interval)}
	if host != "" {
		fields["host"] = jsonvalue.NewString(host)
	}
	return jsonvalue.NewObject(fields)
}

func grrStateWatchFrame(subscriptionID string) pluginprotocol.Frame {
	return pluginprotocol.Frame{Type: "state_watch", RequestID: "watch-" + subscriptionID, SubscriptionID: subscriptionID, WatchArgs: grrWatchArgs("", "40ms"), State: jsonvalue.NewNull()}
}

func grrWaitSearch(t grrReporter, fixture *grrFixture, sequence uint64, timeout time.Duration) grrRequest {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case request := <-fixture.searchCalls:
			if request.Sequence >= sequence {
				return request
			}
		case <-timer.C:
			t.Fatalf("search request %d did not arrive; total=%d", sequence, fixture.searchCountSnapshot())
		}
	}
}

func grrPollCount(t grrReporter, fixture *grrFixture, target uint64, timeout time.Duration) {
	t.Helper()
	if fixture.searchCountSnapshot() >= target {
		return
	}
	_ = grrWaitSearch(t, fixture, target, timeout)
}

func grrParseJSON(t grrReporter, text string) *jsonvalue.Value {
	t.Helper()
	value, err := jsonvalue.Parse([]byte(text), 32_768)
	if err != nil {
		t.Fatalf("parse test JSON: %v", err)
	}
	return value
}

func grrNoGHDirectory(t grrReporter) string {
	t.Helper()
	path, err := os.MkdirTemp(grrSuiteDirectory, "no-gh-")
	if err != nil {
		t.Fatalf("create isolated PATH: %v", err)
	}
	return path
}

func grrBuildRoot(t grrReporter) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func grrExecutableSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
