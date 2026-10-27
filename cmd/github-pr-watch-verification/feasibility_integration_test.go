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
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This fixed round trip proves test-owned TLS, JSON, and cursor controls only.
// It runs no future adapter, plugin, hub, acceptance case or live GitHub call.
// The name deliberately falls outside the acceptance TestIntegration filter.
func TestGitHubPRWatchGraphQLFeasibility(t *testing.T) {
	started := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "PR watch fixture"},
		DNSNames:  []string{"api.github.com", "ghes.example.test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	type requestRecord struct{ host, path, token, after string }
	requests := make(chan requestRecord, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&call) != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		requests <- requestRecord{r.Host, r.URL.Path, r.Header.Get("Authorization"), call.Variables["after"]}
		w.Header().Set("Content-Type", "application/json")
		if call.Variables["after"] == "failed-page" {
			_, _ = io.WriteString(w, `{"data":{"node":null},"errors":[{"message":"fixture page unavailable"}]}`)
			return
		}
		id, next, more := "C1", "cursor-1", true
		if call.Variables["after"] == "cursor-1" {
			id, next, more = "C2", "cursor-2", false
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": map[string]any{
			"comments": map[string]any{
				"nodes":    []map[string]any{{"id": id, "body": "external 日本語\nquoted \"text\"", "replyTo": map[string]string{"id": "root-comment"}}},
				"pageInfo": map[string]any{"hasNextPage": more, "endCursor": next},
			},
		}}})
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		switch address {
		case "api.github.com:443", "ghes.example.test:443", "wrong.example.test:443":
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		default:
			return nil, fmt.Errorf("fixture rejects outbound address %s", address)
		}
	}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	type page struct {
		Data struct {
			Node *struct {
				Comments struct {
					Nodes []struct {
						ID, Body string
						ReplyTo  struct{ ID string }
					}
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
				}
			}
		}
		Errors []struct{ Message string }
	}
	fetch := func(endpoint, token, after string) (page, error) {
		var cursor any
		if after != "" {
			cursor = after
		}
		encoded, encodeErr := json.Marshal(map[string]any{
			"query":     "query($after:String){node(id:\"fixture\"){... on PullRequestReviewThread{comments(first:1,after:$after){nodes{id body replyTo{id}} pageInfo{hasNextPage endCursor}}}}}",
			"variables": map[string]any{"after": cursor},
		})
		if encodeErr != nil {
			return page{}, encodeErr
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(encoded)))
		if requestErr != nil {
			return page{}, requestErr
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, requestErr := client.Do(request)
		if requestErr != nil {
			return page{}, requestErr
		}
		defer response.Body.Close()
		var result page
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&result)
		return result, decodeErr
	}
	for _, target := range []struct{ endpoint, host, path, token string }{
		{"https://api.github.com/graphql", "api.github.com", "/graphql", "github-marker"},
		{"https://ghes.example.test/api/graphql", "ghes.example.test", "/api/graphql", "ghes-marker"},
	} {
		first, fetchErr := fetch(target.endpoint, target.token, "")
		if fetchErr != nil || first.Data.Node == nil || len(first.Data.Node.Comments.Nodes) != 1 || !first.Data.Node.Comments.PageInfo.HasNextPage || first.Data.Node.Comments.PageInfo.EndCursor != "cursor-1" {
			t.Fatalf("first fixed page: error=%v result=%+v", fetchErr, first)
		}
		second, fetchErr := fetch(target.endpoint, target.token, first.Data.Node.Comments.PageInfo.EndCursor)
		if fetchErr != nil || second.Data.Node == nil || len(second.Data.Node.Comments.Nodes) != 1 || second.Data.Node.Comments.PageInfo.HasNextPage {
			t.Fatalf("second fixed page: error=%v result=%+v", fetchErr, second)
		}
		comment := second.Data.Node.Comments.Nodes[0]
		if comment.ID != "C2" || comment.Body != "external 日本語\nquoted \"text\"" || comment.ReplyTo.ID != "root-comment" {
			t.Fatalf("JSON body/relationship round trip changed: %+v", comment)
		}
		for _, after := range []string{"", "cursor-1"} {
			got := <-requests
			if got != (requestRecord{target.host, target.path, "Bearer " + target.token, after}) {
				t.Fatalf("selected origin/token/cursor changed: %+v", got)
			}
		}
	}
	partial, err := fetch("https://api.github.com/graphql", "github-marker", "failed-page")
	if err != nil || partial.Data.Node != nil || len(partial.Errors) != 1 || partial.Errors[0].Message != "fixture page unavailable" {
		t.Fatalf("HTTP-200 partial/error representation: error=%v result=%+v", err, partial)
	}
	<-requests
	if _, err := fetch("https://wrong.example.test/graphql", "must-not-reach-handler", ""); err == nil {
		t.Fatal("certificate hostname verification unexpectedly succeeded")
	}
	select {
	case got := <-requests:
		t.Fatalf("TLS failure exposed a credential to HTTP handler: %+v", got)
	default:
	}
	t.Logf("mechanism-only: two selected HTTPS origins, four cursor pages, HTTP-200 GraphQL error, and certificate rejection in %s; no product acceptance", time.Since(started))
}
