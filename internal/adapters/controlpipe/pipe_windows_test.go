//go:build windows

package controlpipe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

const controlPipeTestSessionID = "11111111-1111-4111-8111-111111111111"

func TestClientIdentityAndResponseClassification(t *testing.T) {
	for _, test := range []struct {
		name, sid, response                    string
		identityErr                            error
		writeCount                             int
		writeErr                               error
		wantUncertain, wantDaemon, wantSuccess bool
	}{
		{name: "success", sid: "SID", response: `{"ok":true,"subscription_id":"opaque"}` + "\n", wantSuccess: true},
		{name: "daemon rejection", sid: "SID", response: `{"ok":false,"code":"watch_rejected","message":"rejected"}` + "\n", wantDaemon: true},
		{name: "unverifiable", identityErr: errors.New("process unavailable")},
		{name: "foreign server", sid: "OTHER"},
		{name: "write failed before bytes", sid: "SID", writeErr: io.ErrClosedPipe},
		{name: "partial write uncertain", sid: "SID", writeCount: 1, writeErr: io.ErrClosedPipe, wantUncertain: true},
		{name: "missing response", sid: "SID", wantUncertain: true},
		{name: "malformed response", sid: "SID", response: "not-json\n", wantUncertain: true},
		{name: "partial response", sid: "SID", response: `{"ok":true}`, wantUncertain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			pipes, conn := NewMockpipeSystem(ctrl), NewMockConn(ctrl)
			ctx := context.Background()
			pipes.EXPECT().Dial(ctx, "pipe").Return(conn, nil)
			conn.EXPECT().Close().Return(nil)
			pipes.EXPECT().ServerUserSID(conn).Return(test.sid, test.identityErr)
			if test.identityErr == nil && test.sid == "SID" {
				conn.EXPECT().Write(gomock.Any()).DoAndReturn(func(data []byte) (int, error) {
					if test.writeErr != nil {
						return test.writeCount, test.writeErr
					}
					return len(data), nil
				}).Times(1)
				if test.writeErr == nil {
					reader := strings.NewReader(test.response)
					conn.EXPECT().Read(gomock.Any()).DoAndReturn(reader.Read).AnyTimes()
				}
			}
			response, _, err := registerWith(ctx, "manual", controlPipeTestSessionID, jsonvalue.NewObject(nil), "pipe", "SID", pipes)
			var uncertain *UncertainError
			var daemon *DaemonError
			if errors.As(err, &uncertain) != test.wantUncertain || errors.As(err, &daemon) != test.wantDaemon || response.Success != test.wantSuccess {
				t.Fatalf("response %#v error %v", response, err)
			}
			if !test.wantSuccess && err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestClientContextDeadlineAndCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	pipes, conn := NewMockpipeSystem(ctrl), NewMockConn(ctrl)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pipes.EXPECT().Dial(ctx, "pipe").Return(conn, nil)
	conn.EXPECT().Close().Return(nil)
	pipes.EXPECT().ServerUserSID(conn).Return("SID", nil)
	requestWritten := make(chan struct{})
	deadlineApplied := make(chan struct{})
	conn.EXPECT().Write(gomock.Any()).DoAndReturn(func(data []byte) (int, error) { close(requestWritten); return len(data), nil })
	conn.EXPECT().SetDeadline(gomock.Any()).DoAndReturn(func(time.Time) error { close(deadlineApplied); return nil })
	conn.EXPECT().Read(gomock.Any()).DoAndReturn(func([]byte) (int, error) { <-requestWritten; cancel(); <-deadlineApplied; return 0, context.Canceled })
	_, _, err := registerWith(ctx, "manual", controlPipeTestSessionID, jsonvalue.NewObject(nil), "pipe", "SID", pipes)
	var uncertain *UncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("error %v want uncertain", err)
	}
}

func TestServerCreationSuppliesRestrictedPolicy(t *testing.T) {
	for _, test := range []struct {
		name                   string
		identityErr, listenErr error
	}{
		{name: "success"}, {name: "identity unavailable", identityErr: errors.New("identity")}, {name: "pipe unavailable or second instance", listenErr: errors.New("first instance exists")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			pipes, listener, service := NewMockpipeSystem(ctrl), NewMockListener(ctrl), NewMockregistrar(ctrl)
			pipes.EXPECT().CurrentUserSID().Return("SID", test.identityErr)
			if test.identityErr == nil {
				pipes.EXPECT().Listen(`\\.\pipe\agent-pulse-hub-v1-SID`, "D:P(A;;GA;;;SID)").Return(listener, test.listenErr)
			}
			server, err := newServer(service, slog.New(slog.NewTextHandler(io.Discard, nil)), pipes)
			if (err != nil) != (test.identityErr != nil || test.listenErr != nil) {
				t.Fatalf("error %v", err)
			}
			if err == nil {
				listener.EXPECT().Close().Return(nil)
				_ = server.Close()
			}
		})
	}
}

func TestServerHandlesRegistrationAndLostResponse(t *testing.T) {
	for _, test := range []struct {
		name      string
		resultErr *hub.Error
		writeErr  error
	}{
		{name: "success"}, {name: "lost response", writeErr: io.ErrClosedPipe}, {name: "plugin rejection", resultErr: &hub.Error{Code: "watch_rejected", Message: "rejected"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			conn, service := NewMockConn(ctrl), NewMockregistrar(ctrl)
			reader := strings.NewReader(`{"version":1,"op":"register","plugin":"manual","session_id":"` + controlPipeTestSessionID + `","watch_args":{}}` + "\n")
			conn.EXPECT().SetDeadline(gomock.Any()).Return(nil)
			conn.EXPECT().Read(gomock.Any()).DoAndReturn(reader.Read).AnyTimes()
			service.EXPECT().Register("manual", controlPipeTestSessionID, gomock.Any()).Return(domain.SubscriptionID("opaque"), test.resultErr)
			conn.EXPECT().Write(gomock.Any()).DoAndReturn(func(data []byte) (int, error) {
				if test.writeErr != nil {
					return 0, test.writeErr
				}
				response, err := ParseRegistrationResponse([]byte(strings.TrimSuffix(string(data), "\n")))
				if err != nil || response.Success != (test.resultErr == nil) {
					t.Errorf("response %#v error %v", response, err)
				}
				return len(data), nil
			})
			server := &Server{service: service, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), active: map[net.Conn]struct{}{}}
			server.handle(conn)
		})
	}
}
