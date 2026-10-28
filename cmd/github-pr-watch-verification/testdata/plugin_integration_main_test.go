//go:build integration

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/adapters/githubplugin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// This file is copied into the plugin's integration build. Only network
// resolution, trusted test roots, timing and protocol observation are replaced.
func TestMain(m *testing.M) {
	if os.Getenv("APH_GPW_PLUGIN_MODE") != "1" {
		os.Exit(m.Run())
	}
	data, err := os.ReadFile(os.Getenv("APH_GPW_TEST_ROOT_CERT"))
	if err != nil {
		os.Exit(2)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		os.Exit(2)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.github.com:443" && address != "ghes.example.test:443" {
			return nil, os.ErrPermission
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, os.Getenv("APH_GPW_TEST_SERVER_ADDRESS"))
	}
	http.DefaultTransport = transport
	wait := func(ctx context.Context, _ time.Duration) bool {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	serveWithOptions([]githubplugin.Option{githubplugin.WithWait(wait)}, grpc.StreamInterceptor(gpwJournalSession))
	os.Exit(0)
}

var gpwJournalMu sync.Mutex

type gpwJournalStream struct{ grpc.ServerStream }

func gpwJournalSession(server any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(server, &gpwJournalStream{stream})
}
func gpwRecord(message any) map[string]any {
	value, ok := message.(*wrapperspb.BytesValue)
	if !ok {
		return nil
	}
	var source map[string]any
	if json.Unmarshal(value.Value, &source) != nil {
		return nil
	}
	result := map[string]any{"wire_bytes": len(value.Value)}
	if data, ok := source["data"]; ok {
		encoded, _ := json.Marshal(data)
		result["chunk_bytes"] = len(encoded)
	}
	for _, key := range []string{"type", "request_id", "subscription_id", "event_id", "generation", "status", "quiesce", "accepted", "schema", "index", "chunk_count"} {
		if value, ok := source[key]; ok {
			result[key] = value
		}
	}
	return result
}
func gpwAppend(row map[string]any) {
	path := os.Getenv("APH_GPW_PROTOCOL_JOURNAL")
	if path == "" || row == nil {
		return
	}
	gpwJournalMu.Lock()
	defer gpwJournalMu.Unlock()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_ = json.NewEncoder(file).Encode(row)
}
func (s *gpwJournalStream) SendMsg(message any) error {
	row := gpwRecord(message)
	if row != nil && (row["type"] == "checkpoint_result" || row["type"] == "checkpoint_stream_end") {
		hold := os.Getenv("APH_GPW_HOLD_CHECKPOINT_FILE")
		if _, err := os.Stat(hold); hold != "" && err == nil {
			gpwAppend(map[string]any{"type": "checkpoint_held"})
			for {
				if _, err := os.Stat(hold); err != nil {
					break
				}
				select {
				case <-s.Context().Done():
					return s.Context().Err()
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
	}
	if err := s.ServerStream.SendMsg(message); err != nil {
		return err
	}
	gpwAppend(row)
	return nil
}
func (s *gpwJournalStream) RecvMsg(message any) error {
	if err := s.ServerStream.RecvMsg(message); err != nil {
		return err
	}
	gpwAppend(gpwRecord(message))
	return nil
}
