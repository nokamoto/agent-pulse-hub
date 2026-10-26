//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
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
		serve(grpc.StreamInterceptor(journalGRRSession))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type grrJournalStream struct {
	grpc.ServerStream
	mu sync.Mutex
}

type grrProtocolRecord struct {
	Type           string `json:"type"`
	RequestID      string `json:"request_id"`
	SubscriptionID string `json:"subscription_id"`
	Accepted       *bool  `json:"accepted,omitempty"`
}

func journalGRRSession(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if info.FullMethod != pluginprotocol.SessionMethod || os.Getenv("APH_GRR_PROTOCOL_JOURNAL") == "" {
		return handler(server, stream)
	}
	return handler(server, &grrJournalStream{ServerStream: stream})
}

func (s *grrJournalStream) SendMsg(message any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ServerStream.SendMsg(message); err != nil {
		return err
	}
	return s.record(message, pluginprotocol.PluginToDaemon)
}

func (s *grrJournalStream) RecvMsg(message any) error {
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.record(message, pluginprotocol.DaemonToPlugin)
}

func (s *grrJournalStream) record(message any, direction pluginprotocol.Direction) error {
	value, ok := message.(*wrapperspb.BytesValue)
	if !ok {
		return nil
	}
	frame, err := pluginprotocol.ParsePayload(value.Value, direction)
	if err != nil {
		return err
	}
	// Only protocol correlations are recorded, never credentials, state or URLs.
	file, err := os.OpenFile(os.Getenv("APH_GRR_PROTOCOL_JOURNAL"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := json.NewEncoder(file).Encode(grrProtocolRecord{Type: frame.Type, RequestID: frame.RequestID, SubscriptionID: frame.SubscriptionID, Accepted: frame.Accepted})
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
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
