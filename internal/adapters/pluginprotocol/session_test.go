package pluginprotocol

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type echoSessionServer struct{}

func (echoSessionServer) Session(stream SessionServer) error {
	payload, err := stream.Recv()
	if err != nil {
		return err
	}
	return stream.Send(payload)
}

func TestSessionTransportsOneRawBytesValue(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(ServerOptions()...)
	RegisterEventPluginServer(server, echoSessionServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///plugin-session-test",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := OpenSession(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"version":1,"type":"ready"}`)
	if err := session.Send(want); err != nil {
		t.Fatal(err)
	}
	got, err := session.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Session payload = %q, want raw bytes %q", got, want)
	}
	if err := session.CloseSend(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRejectsOversizedApplicationPayloadBeforeSend(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(ServerOptions()...)
	RegisterEventPluginServer(server, echoSessionServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient("passthrough:///plugin-session-limit-test",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, err := OpenSession(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Send(make([]byte, MaxPayloadBytes+1)); err == nil {
		t.Fatal("Session accepted payload over the application limit")
	}
}
