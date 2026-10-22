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
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This probe qualifies only test-owned TLS routing/trust and gh subprocess
// mechanics. It runs no production plugin, hub, acceptance case or live service.
// Its name intentionally falls outside the TestIntegration acceptance filter.
func TestGitHubReviewRequestFeasibility(t *testing.T) {
	started := time.Now()
	t.Run("verified-TLS-at-original-API-host", probeVerifiedTLS)
	t.Run("host-specific-gh-argv-and-child-environment", probeGHProcess)
	t.Logf("mechanism-only elapsed=%s; no product acceptance executed", time.Since(started))
}

func probeVerifiedTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "GRR fixture"},
		DNSNames:  []string{"api.github.com"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Host + " " + r.URL.RequestURI() + " " + r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"login":"fixture-user"}`)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)

	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	makeClient := func(pool *x509.CertPool) *http.Client {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// This is test-only bootstrap configuration. The production adapter keeps
		// its original HTTPS URL, HTTP wire behavior and certificate verification.
		transport.Proxy = nil
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "api.github.com:443" && address != "wrong.example.test:443" {
				return nil, fmt.Errorf("fixture disallows outbound address %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		}
		t.Cleanup(transport.CloseIdleConnections)
		return &http.Client{Transport: transport, Timeout: 3 * time.Second}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user?probe=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fixture-token")
	response, err := makeClient(roots).Do(request)
	if err != nil {
		t.Fatalf("verified fixture TLS: %v", err)
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK || string(data) != `{"login":"fixture-user"}` {
		t.Fatalf("TLS response status=%d body=%q read=%v close=%v", response.StatusCode, data, readErr, closeErr)
	}
	if got := <-requests; got != "api.github.com /user?probe=1 Bearer fixture-token" {
		t.Fatalf("original API request changed: %s", got)
	}
	if _, err := makeClient(x509.NewCertPool()).Do(request.Clone(ctx)); err == nil {
		t.Fatal("untrusted fixture certificate was accepted")
	} else {
		var unknown x509.UnknownAuthorityError
		if !errors.As(err, &unknown) {
			t.Fatalf("expected untrusted CA failure: %v", err)
		}
	}
	wrongHost, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://wrong.example.test/user", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := makeClient(roots).Do(wrongHost); err == nil {
		t.Fatal("wrong TLS hostname was accepted")
	} else {
		var hostname x509.HostnameError
		if !errors.As(err, &hostname) {
			t.Fatalf("expected hostname verification failure: %v", err)
		}
	}
	if len(requests) != 0 {
		t.Fatal("failed TLS verification reached authenticated HTTP handler")
	}
	t.Log("original api.github.com SAN/URL/Host/path/auth preserved; unknown CA and wrong hostname rejected; no external connection")
}

func probeGHProcess(t *testing.T) {
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix, err := exec.CommandContext(ctx, "go", "env", "GOEXE").Output()
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(workspace, "gh"+strings.TrimSpace(string(suffix)))
	buildStarted := time.Now()
	build := exec.CommandContext(ctx, "go", "build", "-o", executable, "./testdata/gh.go")
	build.WaitDelay = time.Second
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build controlled gh: %v\n%s", err, output)
	}
	t.Logf("compiled one external gh fixture: %s", time.Since(buildStarted))
	// Go's exec.LookPath uses this process's PATH, before Cmd.Env is applied.
	// Resolve only after setting the isolated PATH; do not assume Cmd.Env
	// controls lookup. Each real plugin process receives its own isolated PATH.
	t.Setenv("PATH", workspace)
	for _, key := range blockedGHEnvironment {
		t.Setenv(key, "poison-fixture-value")
	}
	resolved, err := exec.LookPath("gh")
	if err != nil || resolved != executable {
		t.Fatalf("isolated gh lookup=%q error=%v", resolved, err)
	}
	journal := filepath.Join(workspace, "gh-journal.json")
	arguments := []string{"auth", "token", "--hostname", "ghes.example.test"}
	command := exec.CommandContext(ctx, resolved, arguments...)
	command.Env = sanitizedGHEnvironment(os.Environ(), journal)
	command.WaitDelay = time.Second
	output, err := command.Output()
	if err != nil || string(output) != "fixture-saved-token\n" {
		t.Fatalf("controlled saved login output=%q error=%v", output, err)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Args    []string        `json:"argv"`
		Blocked map[string]bool `json:"blocked_environment_present"`
	}
	if err := json.Unmarshal(data, &row); err != nil || !reflect.DeepEqual(row.Args, arguments) {
		t.Fatalf("saved-login argv=%q error=%v", row.Args, err)
	}
	for _, key := range blockedGHEnvironment {
		if present, found := row.Blocked[key]; !found || present {
			t.Fatalf("poison environment %s was not removed", key)
		}
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("gh"); err == nil {
		t.Fatal("isolated missing-gh PATH unexpectedly resolved a command")
	}
	t.Log("host argv preserved, all five credential/host overrides removed, direct child reaped, missing-gh lookup confirmed")
}

var blockedGHEnvironment = []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"}

func sanitizedGHEnvironment(parent []string, journal string) []string {
	result := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		key, _, found := strings.Cut(entry, "=")
		if !found || strings.EqualFold(key, "APH_GRR_FIXTURE_JOURNAL") {
			continue
		}
		blocked := false
		for _, name := range blockedGHEnvironment {
			if strings.EqualFold(key, name) {
				blocked = true
				break
			}
		}
		if !blocked {
			result = append(result, entry)
		}
	}
	return append(result, "APH_GRR_FIXTURE_JOURNAL="+journal)
}
