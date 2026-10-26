package pluginprocess

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func acceptV2Watch(t *testing.T, p *Process, session *memorySession, id string) {
	t.Helper()
	p.mu.Lock()
	p.capabilities[pluginprotocol.RuntimeStateV2Capability] = true
	p.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		result <- p.Watch(context.Background(), hub.WatchRequest{
			RequestID: "watch-" + id, SubscriptionID: domain.SubscriptionID(id), Arguments: jsonObject(t, `{}`), RuntimeState: true,
		}, func() error { return nil })
	}()
	if frame := readFrame(t, session); frame.Type != "state_watch" {
		t.Fatalf("v2 watch = %+v", frame)
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "watch_result", RequestID: "watch-" + id, Accepted: boolPointer(true)})
	if err := await(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestV2DormantEventIsDiscardedOnceAndGrantAllowsFreshEvents(t *testing.T) {
	var events atomic.Int32
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{Event: func(_, _, _ string) string {
		events.Add(1)
		return "accepted"
	}})
	acceptV2Watch(t, p, session, "s1")
	dormant := pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s1", EventID: "1", Context: "early"}
	p.handleFrame(dormant)
	if notice := readFrame(t, session); notice.Type != "delivery_notice" || notice.EventID != "1" || notice.Status != "discarded" {
		t.Fatalf("dormant terminal notice = %+v", notice)
	}
	p.handleFrame(dormant)
	p.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "foreign", EventID: "1", Context: "foreign"})
	if events.Load() != 0 || len(session.outgoing) != 0 {
		t.Fatal("dormant, repeated or foreign event was admitted or received an extra notice")
	}
	if err := p.ActivateWatch(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if grant := readFrame(t, session); grant.Type != "activate_watch" || grant.SubscriptionID != "s1" {
		t.Fatalf("activation grant = %+v", grant)
	}
	if err := p.ActivateWatch(context.Background(), "s1"); err != nil || len(session.outgoing) != 0 {
		t.Fatalf("duplicate grant was sent: %v", err)
	}
	p.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s1", EventID: "2", Context: "fresh"})
	if notice := readFrame(t, session); notice.EventID != "2" || notice.Status != "accepted" || events.Load() != 1 {
		t.Fatalf("fresh event after grant = %+v, events = %d", notice, events.Load())
	}
}

func TestV2DormantCheckpointDoesNotGrantAndQuiescePermanentlyClosesActivation(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	acceptV2Watch(t, p, session, "s1")
	for _, quiesce := range []bool{false, true} {
		result := make(chan error, 1)
		go func() {
			_, err := p.Checkpoint(context.Background(), hub.RuntimeCheckpointRequest{SubscriptionID: "s1", Quiesce: quiesce})
			result <- err
		}()
		checkpoint := readFrame(t, session)
		if checkpoint.Type != "checkpoint" || checkpoint.Quiesce != quiesce {
			t.Fatalf("dormant checkpoint = %+v", checkpoint)
		}
		// Checkpoint has entered the serialized host stream. Before replying,
		// a quiescing request must already prevent a later activation send.
		if quiesce {
			if err := p.ActivateWatch(context.Background(), "s1"); err != nil || len(session.outgoing) != 0 {
				t.Fatalf("quiescing dormant watch received grant: %v", err)
			}
		}
		session.incoming <- payload(t, pluginprotocol.Frame{
			Type: "checkpoint_result", RequestID: checkpoint.RequestID,
			Accepted: boolPointer(true), State: jsonObject(t, `{"schema":"v1","payload":{}}`),
		})
		if err := await(t, result); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		granted := p.watches["s1"].granted
		p.mu.Unlock()
		if granted {
			t.Fatal("checkpoint activated a dormant watch")
		}
	}
	if err := p.ActivateWatch(context.Background(), "s1"); err != nil || len(session.outgoing) != 0 {
		t.Fatalf("quiesced watch received grant: %v", err)
	}
}

func TestActivationGrantDeadlineIncludesStreamQueueAndCancelsUnsentGrant(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop cancellation", true: "batch expiry"}[expire], func(t *testing.T) {
			p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
			acceptV2Watch(t, p, session, "s1")
			p.writeGate <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			if expire {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			defer cancel()
			if err := p.ActivateWatch(ctx, "s1"); err == nil {
				t.Fatal("cancelled grant succeeded")
			}
			p.releaseWrite()
			if len(session.outgoing) != 0 || processFailed(p) != expire {
				t.Fatalf("unsent grant: sent=%d unavailable=%v", len(session.outgoing), processFailed(p))
			}
		})
	}
}

func TestActivationGrantSendFailureInvalidatesInputAndPreservesV1Mode(t *testing.T) {
	p, session := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	acceptV2Watch(t, p, session, "s1")
	if err := p.ActivateWatch(context.Background(), "foreign"); err == nil || len(session.outgoing) != 0 {
		t.Fatalf("unaccepted grant = %v", err)
	}
	session.sendErr = errors.New("injected activation transport failure")
	if err := p.ActivateWatch(context.Background(), "s1"); err == nil || !processFailed(p) {
		t.Fatalf("activation send failure = %v, unavailable = %v", err, processFailed(p))
	}
	legacy, legacySession := readyProcess(t, domain.InputRole, hub.PluginHandlers{})
	legacy.capabilities[pluginprotocol.RuntimeStateCapability] = true
	legacy.watches["s1"] = &watchState{active: true, stateful: true}
	if err := legacy.ActivateWatch(context.Background(), "s1"); err == nil || len(legacySession.outgoing) != 0 || processFailed(legacy) {
		t.Fatalf("legacy activation changed v1 mode: %v", err)
	}
}
