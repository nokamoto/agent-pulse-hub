package pluginprotocol

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	ServiceName     = "agentpulse.plugin.v1.EventPlugin"
	SessionMethod   = "/" + ServiceName + "/Session"
	MaxPayloadBytes = 65_536
	// MaxMessageBytes includes the four-byte protobuf BytesValue field envelope.
	// The gRPC transport frame is excluded from gRPC message-size options.
	MaxMessageBytes = MaxPayloadBytes + 4
)

// SessionServer exchanges complete JSON payload bytes, without delimiters.
// Send calls are serialized. Callers must use a single receiver per session.
// Receiving deliberately leaves JSON validation to ParsePayload so a malformed
// size-bounded message does not prevent receiving the next message.
type SessionServer interface {
	Send([]byte) error
	Recv() ([]byte, error)
	Context() context.Context
}

// SessionClient is the daemon side of a bidirectional Session.
type SessionClient interface {
	SessionServer
	CloseSend() error
}

// EventPluginServer implements the application's sole gRPC method. Lifecycle
// rules, including rejecting a second active session, belong to its handler.
type EventPluginServer interface {
	Session(SessionServer) error
}

// EventPluginServiceDesc registers the language-neutral BytesValue stream
// without generated product protobuf messages.
var EventPluginServiceDesc = grpc.ServiceDesc{
	ServiceName: ServiceName,
	HandlerType: (*EventPluginServer)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "Session",
		Handler:       sessionHandler,
		ServerStreams: true,
		ClientStreams: true,
	}},
}

func RegisterEventPluginServer(registrar grpc.ServiceRegistrar, server EventPluginServer) {
	registrar.RegisterService(&EventPluginServiceDesc, server)
}

func sessionHandler(server any, stream grpc.ServerStream) error {
	wire := &grpc.GenericServerStream[wrapperspb.BytesValue, wrapperspb.BytesValue]{ServerStream: stream}
	return server.(EventPluginServer).Session(&payloadSession{wire: wire})
}

// ServerOptions bounds binary protobuf messages in both directions. Use these
// options when constructing the application's gRPC server.
func ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxMessageBytes),
		grpc.MaxSendMsgSize(MaxMessageBytes),
	}
}

// ClientCallOptions bounds encoded protobuf messages for the application RPC.
func ClientCallOptions() []grpc.CallOption {
	return []grpc.CallOption{
		grpc.MaxCallRecvMsgSize(MaxMessageBytes),
		grpc.MaxCallSendMsgSize(MaxMessageBytes),
	}
}

// OpenSession opens the one application stream on a framework-owned connection.
// Payload and protobuf limits are applied independently. Explicit call options
// cannot raise the protocol's message limits.
func OpenSession(ctx context.Context, connection grpc.ClientConnInterface, options ...grpc.CallOption) (SessionClient, error) {
	options = append(append([]grpc.CallOption(nil), options...), ClientCallOptions()...)
	stream, err := connection.NewStream(ctx, &EventPluginServiceDesc.Streams[0], SessionMethod, options...)
	if err != nil {
		return nil, err
	}
	wire := &grpc.GenericClientStream[wrapperspb.BytesValue, wrapperspb.BytesValue]{ClientStream: stream}
	return &payloadClient{payloadSession: payloadSession{wire: wire}, stream: stream}, nil
}

type wireSession interface {
	Send(*wrapperspb.BytesValue) error
	Recv() (*wrapperspb.BytesValue, error)
	Context() context.Context
}

type payloadSession struct {
	wire   wireSession
	sendMu sync.Mutex
}

func (s *payloadSession) Send(payload []byte) error {
	if err := validatePayloadSize(payload); err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.wire.Send(wrapperspb.Bytes(payload))
}

func (s *payloadSession) Recv() ([]byte, error) {
	message, err := s.wire.Recv()
	if err != nil {
		return nil, err
	}
	payload := message.GetValue()
	if err := validatePayloadSize(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *payloadSession) Context() context.Context {
	return s.wire.Context()
}

type payloadClient struct {
	payloadSession
	stream grpc.ClientStream
}

func (s *payloadClient) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.CloseSend()
}

func validatePayloadSize(payload []byte) error {
	if len(payload) > MaxPayloadBytes {
		return status.Errorf(codes.ResourceExhausted, "plugin payload exceeds %d bytes", MaxPayloadBytes)
	}
	return nil
}
