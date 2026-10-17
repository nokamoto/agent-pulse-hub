//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The integration test executable also runs the actual product Session when
// launched by go-plugin. Its TLS routing is available only in this test build.
func TestMain(m *testing.M) {
	if os.Getenv("APH_GRR_LOOKUP_MODE") == "1" {
		path, err := exec.LookPath("gh")
		result := fmt.Sprintf("path=%s\nPATH=%s\nPATHEXT=%s\nerror=%v", path, os.Getenv("PATH"), os.Getenv("PATHEXT"), err)
		_ = os.WriteFile(os.Getenv("APH_GRR_LOOKUP_JOURNAL"), []byte(result), 0o600)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("APH_GRR_PLUGIN_MODE") == "1" {
		configureFixtureTransport()
		serve()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func configureFixtureTransport() {
	certificatePEM, err := os.ReadFile(os.Getenv("APH_GRR_TEST_ROOT_CERT"))
	if err != nil {
		os.Exit(2)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		os.Exit(2)
	}
	address := os.Getenv("APH_GRR_TEST_SERVER_ADDRESS")
	if address == "" {
		os.Exit(2)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != "api.github.com:443" && target != "ghes.example.test:443" {
			return nil, os.ErrPermission
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = transport
}
