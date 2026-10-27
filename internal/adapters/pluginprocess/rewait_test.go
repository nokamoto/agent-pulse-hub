package pluginprocess

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func readyRewaitProcess(t *testing.T, handlers hub.PluginHandlers) (*Process, *memorySession) {
	t.Helper()
	p, session := readyProcess(t, domain.InputRole, handlers)
	acceptV2Watch(t, p, session, "s")
	p.mu.Lock()
	p.capabilities[pluginprotocol.RewaitCapability] = true
	p.mu.Unlock()
	return p, session
}

func TestRewaitProcessStagesPositionBeforeMatchingGrantAndFencesEventIDs(t *testing.T) {
	var events atomic.Int32
	p, session := readyRewaitProcess(t, hub.PluginHandlers{StateEvent: func(_, _, generation, _ string) string {
		if generation == "g2" {
			events.Add(1)
			return "accepted"
		}
		return "discarded"
	}})
	if err := p.ActivateGeneration(context.Background(), "s", "g1"); err != nil {
		t.Fatal(err)
	}
	if grant := readFrame(t, session); grant.Generation != "g1" {
		t.Fatalf("grant %+v", grant)
	}
	prepared := make(chan error, 1)
	go func() {
		prepared <- p.ResumeWatch(context.Background(), hub.ResumeWatchRequest{RequestID: "r", SubscriptionID: "s", Generation: "g2", ConfirmationPosition: "private-position"})
	}()
	if frame := readFrame(t, session); frame.Type != "resume_watch" || frame.Generation != "g2" || frame.ConfirmationPosition != "private-position" {
		t.Fatalf("prepare %+v", frame)
	}
	if err := p.ActivateGeneration(context.Background(), "s", "g2"); err == nil {
		t.Fatal("grant without positive preparation succeeded")
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "resume_result", RequestID: "r", Accepted: boolPointer(true)})
	if err := await(t, prepared); err != nil {
		t.Fatal(err)
	}
	if len(session.outgoing) != 0 {
		t.Fatal("positive preparation emitted a grant")
	}
	if err := p.ActivateGeneration(context.Background(), "s", "g2"); err != nil {
		t.Fatal(err)
	}
	if grant := readFrame(t, session); grant.Generation != "g2" {
		t.Fatalf("grant %+v", grant)
	}
	if err := p.ActivateGeneration(context.Background(), "s", "g2"); err != nil || len(session.outgoing) != 0 {
		t.Fatalf("duplicate grant %v", err)
	}
	p.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s", EventID: "1", Generation: "g1", Context: "stale"})
	if notice := readFrame(t, session); notice.Status != "discarded" {
		t.Fatalf("stale %+v", notice)
	}
	p.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s", EventID: "2", Generation: "g2", Context: "fresh"})
	if notice := readFrame(t, session); notice.Status != "accepted" || events.Load() != 1 {
		t.Fatalf("fresh %+v", notice)
	}
	p.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s", EventID: "2", Generation: "g2", Context: "repeat"})
	if len(session.outgoing) != 0 || events.Load() != 1 {
		t.Fatal("duplicate event received another notice")
	}
}

func TestRewaitProcessNegativePreparationIsTypedAndLeavesGeneration(t *testing.T) {
	p, session := readyRewaitProcess(t, hub.PluginHandlers{})
	if err := p.ActivateGeneration(context.Background(), "s", "old"); err != nil {
		t.Fatal(err)
	}
	readFrame(t, session)
	result := make(chan error, 1)
	go func() {
		result <- p.ResumeWatch(context.Background(), hub.ResumeWatchRequest{RequestID: "r", SubscriptionID: "s", Generation: "new", ConfirmationPosition: "p"})
	}()
	readFrame(t, session)
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "resume_result", RequestID: "r", Accepted: boolPointer(false), Error: "continuity_expired"})
	var rejected *hub.PositionRejectedError
	if err := await(t, result); !errors.As(err, &rejected) {
		t.Fatalf("negative result %v", err)
	}
	if err := p.ActivateGeneration(context.Background(), "s", "new"); err == nil || len(session.outgoing) != 0 {
		t.Fatal("rejected preparation granted")
	}
	if processFailed(p) {
		t.Fatal("ordinary position rejection invalidated input")
	}
}

func TestRewaitProcessCancelledPreparationExpiresCorrelationWithoutLosingFinalCheckpoint(t *testing.T) {
	p, session := readyRewaitProcess(t, hub.PluginHandlers{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- p.ResumeWatch(ctx, hub.ResumeWatchRequest{RequestID: "r", SubscriptionID: "s", Generation: "new", ConfirmationPosition: "p"})
	}()
	readFrame(t, session)
	cancel()
	if err := await(t, result); err == nil {
		t.Fatal("cancelled preparation succeeded")
	}
	if processFailed(p) {
		t.Fatal("stop cancellation invalidated the input before final checkpoint")
	}
	p.handleFrame(pluginprotocol.Frame{Type: "resume_result", RequestID: "r", Accepted: boolPointer(true)})
	p.mu.Lock()
	staged := p.watches["s"].stagedGeneration
	p.mu.Unlock()
	if staged != "" {
		t.Fatal("late preparation result was applied")
	}
	checkpointed := make(chan error, 1)
	go func() {
		_, err := p.Checkpoint(context.Background(), hub.RuntimeCheckpointRequest{SubscriptionID: "s", Quiesce: true})
		checkpointed <- err
	}()
	frame := readFrame(t, session)
	if frame.Type != "checkpoint" || !frame.Quiesce {
		t.Fatalf("final checkpoint %+v", frame)
	}
	session.incoming <- payload(t, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolPointer(true), State: jsonObject(t, `{"schema":"v1","payload":{}}`)})
	if err := await(t, checkpointed); err != nil {
		t.Fatal(err)
	}
}

func TestRewaitGenerationFieldIsConditionalOnCapability(t *testing.T) {
	var events atomic.Int32
	p, session := readyRewaitProcess(t, hub.PluginHandlers{StateEvent: func(_, _, _, _ string) string { events.Add(1); return "accepted" }})
	if err := p.ActivateGeneration(context.Background(), "s", "g"); err != nil {
		t.Fatal(err)
	}
	readFrame(t, session)
	p.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s", EventID: "1", Context: "missing generation"})
	if events.Load() != 0 || len(session.outgoing) != 0 {
		t.Fatal("missing generation admitted")
	}
	legacy, legacySession := readyProcess(t, domain.InputRole, hub.PluginHandlers{Event: func(_, _, _ string) string { events.Add(1); return "accepted" }})
	acceptV2Watch(t, legacy, legacySession, "s")
	legacy.handleFrame(pluginprotocol.Frame{Type: "state_event", SubscriptionID: "s", EventID: "1", Generation: "unexpected", Context: "c"})
	if events.Load() != 0 || len(legacySession.outgoing) != 0 {
		t.Fatal("nonparticipating generation accepted")
	}
}

func TestRewaitCancelledGrantDoesNotEnterStream(t *testing.T) {
	p, session := readyRewaitProcess(t, hub.PluginHandlers{})
	if err := p.ActivateGeneration(context.Background(), "s", "old"); err != nil {
		t.Fatal(err)
	}
	readFrame(t, session)
	p.mu.Lock()
	p.watches["s"].stagedGeneration = "new"
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.ActivateGeneration(ctx, "s", "new"); err == nil {
		t.Fatal("cancelled grant succeeded")
	}
	if len(session.outgoing) != 0 || processFailed(p) {
		t.Fatal("stop cancellation sent an unsent grant or invalidated the input")
	}
	p.mu.Lock()
	generation := p.watches["s"].generation
	p.mu.Unlock()
	if generation != "old" {
		t.Fatal("cancelled grant changed transport generation")
	}
}
