package pluginprocess

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

type checkpointTransfer struct {
	ctx          context.Context
	sink         hub.StreamCheckpointSink
	begun        bool
	count, total uint64
}
type envelopeSource struct {
	schema string
	data   *jsonvalue.Value
}

func (s envelopeSource) Schema() string                         { return s.schema }
func (s envelopeSource) Count() uint64                          { return 1 }
func (s envelopeSource) Streamed() bool                         { return false }
func (s envelopeSource) Chunk(uint64) (*jsonvalue.Value, error) { return s.data, nil }

func (p *Process) WatchStream(ctx context.Context, request hub.WatchRequest, source hub.RuntimeSource, activate func() error) error {
	if p.config.Role != domain.InputRole || !request.RuntimeState || activate == nil {
		return &hub.WatchError{Code: "invalid_request", Message: "stream watch requires input runtime-state activation"}
	}
	p.mu.Lock()
	supported := p.capabilities[pluginprotocol.RuntimeStateV2Capability] && p.capabilities[pluginprotocol.RuntimeStateStreamCapability]
	p.mu.Unlock()
	if !supported {
		return &hub.WatchError{Code: "invalid_request", Message: "stream state capability unavailable"}
	}
	if source == nil && request.State != nil && request.State.Kind() != jsonvalue.Null {
		if err := pluginprotocol.ValidateStateEnvelope(request.State); err != nil {
			return err
		}
		schema, _ := request.State.Get("schema")
		name, _ := jsonvalue.ParseString(schema)
		data, _ := request.State.Get("payload")
		source = envelopeSource{schema: name, data: data}
	}
	schema, count := "", uint64(0)
	if source != nil {
		schema, count = source.Schema(), source.Count()
		if count == 0 {
			return errors.New("stream source must not be empty")
		}
	}
	watch := &watchState{stateful: true, activationRequired: true, pending: make(map[string]bool), notices: make(map[string]*noticeSend)}
	frame, err := p.requestExtended(ctx, pluginprotocol.Frame{Type: "state_stream_watch", RequestID: request.RequestID, SubscriptionID: string(request.SubscriptionID), WatchArgs: request.Arguments, Schema: schema, ChunkCount: strconv.FormatUint(count, 10)}, "watch_result", registrationTimeout, activate, string(request.SubscriptionID), watch, nil, nil, func(ctx context.Context, pending *pendingRequest) error {
		for i := uint64(0); i < count; i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			data, err := source.Chunk(i)
			if err != nil {
				return err
			}
			if err := p.sendRestoreFrame(ctx, pluginprotocol.Frame{Type: "state_stream_chunk", RequestID: request.RequestID, SubscriptionID: string(request.SubscriptionID), Index: strconv.FormatUint(i, 10), Data: data}); err != nil {
				return err
			}
		}
		// The end send and positive response must be ordered even when Recv runs
		// before Send returns. The write gate already orders the host handoff.
		return p.sendRestoreFrameWith(ctx, pluginprotocol.Frame{Type: "state_stream_end", RequestID: request.RequestID, SubscriptionID: string(request.SubscriptionID), ChunkCount: strconv.FormatUint(count, 10)}, func() { p.mu.Lock(); pending.restoreEnd = true; p.mu.Unlock() })
	}, nil)
	if err != nil {
		return &hub.WatchError{Code: "plugin_unavailable", Message: "stream watch could not be restored", Unavailable: isUnavailable(err), Cause: err}
	}
	if frame.Accepted == nil || !*frame.Accepted {
		return &hub.WatchError{Code: "watch_rejected", Message: "stream watch rejected: " + safeText(frame.Error, 256)}
	}
	return nil
}

func (p *Process) sendRestoreFrame(ctx context.Context, frame pluginprotocol.Frame) error {
	return p.sendRestoreFrameWith(ctx, frame, nil)
}

func (p *Process) sendRestoreFrameWith(ctx context.Context, frame pluginprotocol.Frame, onSending func()) error {
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return err
	}
	if err := p.acquireWrite(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	stream := p.stream
	available := !p.failed && !p.stopping && ctx.Err() == nil
	p.mu.Unlock()
	if !available {
		p.releaseWrite()
		return errors.New("plugin unavailable during restore")
	}
	if onSending != nil {
		onSending()
	}
	sendErr, pending := p.sendUntil(ctx, stream, payload, nil)
	if !pending {
		p.releaseWrite()
	}
	return sendErr
}

func (p *Process) CheckpointStream(ctx context.Context, request hub.RuntimeCheckpointRequest, sink hub.StreamCheckpointSink) error {
	p.mu.Lock()
	watch := p.watches[string(request.SubscriptionID)]
	supported := p.capabilities[pluginprotocol.RuntimeStateStreamCapability]
	if previous := p.streamFinishedWatch[string(request.SubscriptionID)]; previous != "" {
		delete(p.streamFinished, previous)
		delete(p.streamFinishedWatch, string(request.SubscriptionID))
	}
	p.mu.Unlock()
	if watch == nil || !watch.stateful || !supported {
		return &hub.RuntimeError{Code: "invalid_watch", Message: "stream checkpoint capability or watch unavailable"}
	}
	watch.orderMu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			watch.orderMu.Unlock()
		}
	}
	defer unlock()
	p.mu.Lock()
	quiesced := watch.quiesced || watch.cancelling || watch.cancelSent || p.watches[string(request.SubscriptionID)] != watch
	p.mu.Unlock()
	if quiesced {
		return &hub.RuntimeError{Code: "invalid_watch", Message: "watch is already quiescent"}
	}
	id, err := randomUUID()
	if err != nil {
		return err
	}
	transfer := &checkpointTransfer{sink: sink}
	_, err = p.requestExtended(ctx, pluginprotocol.Frame{Type: "checkpoint", RequestID: id, SubscriptionID: string(request.SubscriptionID), Quiesce: request.Quiesce}, "checkpoint_result", registrationTimeout, nil, string(request.SubscriptionID), nil, func() {
		if request.Quiesce {
			p.mu.Lock()
			watch.activationClosed = true
			watch.quiesced = true
			p.mu.Unlock()
		}
		unlock()
	}, transfer, nil, nil)
	if err != nil {
		if request.Quiesce && requestWasSent(err) {
			p.mu.Lock()
			watch.activationClosed = true
			watch.quiesced = true
			p.mu.Unlock()
		}
		sink.Abort(err)
		return &hub.RuntimeError{Code: "checkpoint_failed", Message: "stream checkpoint did not complete: " + safeText(err.Error(), 256), Unavailable: isUnavailable(err), Cause: err}
	}
	return nil
}

func (p *Process) expireCheckpoint(id string, pending *pendingRequest, cause error) {
	p.mu.Lock()
	if p.pending[id] != pending {
		p.mu.Unlock()
		return
	}
	delete(p.pending, id)
	p.mu.Unlock()
	if pending.stream != nil {
		pending.stream.sink.Abort(cause)
	}
	pending.result <- requestResult{err: &requestError{message: "stream checkpoint rejected: " + safeText(cause.Error(), 256), cause: cause, sent: pending.sent}}
}

func (p *Process) handleCheckpointStream(frame pluginprotocol.Frame) {
	p.mu.Lock()
	pending := p.pending[frame.RequestID]
	if pending == nil {
		finished := p.streamFinished[frame.RequestID]
		p.mu.Unlock()
		if finished != nil {
			finished.sink.Abort(errors.New("excess data after checkpoint end"))
			p.rejectFrame("excess data after checkpoint end")
		}
		return
	}
	if p.failed || p.stopping || pending == nil || pending.stream == nil || !pending.sent || pending.claimed {
		p.mu.Unlock()
		return
	}
	transfer := pending.stream
	if !time.Now().Before(pending.deadline) || frame.SubscriptionID != pending.watchID {
		p.mu.Unlock()
		p.expireCheckpoint(frame.RequestID, pending, errors.New("stream checkpoint owner or deadline mismatch"))
		return
	}
	var err error
	var raw []byte
	var count uint64
	switch frame.Type {
	case "checkpoint_stream_begin":
		if transfer.begun {
			err = errors.New("duplicate checkpoint begin")
		} else {
			transfer.begun = true
		}
	case "checkpoint_stream_chunk":
		index, _ := pluginprotocol.StreamCounter(frame.Index, false)
		raw = frame.Data.Marshal()
		budget := p.config.RuntimeStateMaxBytes
		if budget == 0 {
			budget = hub.DefaultRuntimeStateMaxBytes
		}
		if uint64(len(raw)) > budget-transfer.total {
			err = fmt.Errorf("runtime-state-max-bytes budget %d exhausted: retained %d, next object %d bytes", budget, transfer.total, len(raw))
		} else if !transfer.begun || index != transfer.count || transfer.count == ^uint64(0) {
			err = errors.New("checkpoint chunk index or ordering exhausted")
		} else {
			transfer.count++
			transfer.total += uint64(len(raw))
		}
	case "checkpoint_stream_end":
		count, _ = pluginprotocol.StreamCounter(frame.ChunkCount, true)
		if !transfer.begun || count == 0 || count != transfer.count {
			err = errors.New("checkpoint end count mismatch")
		} else {
			pending.claimed = true
		}
	}
	p.mu.Unlock()
	if err != nil {
		p.expireCheckpoint(frame.RequestID, pending, err)
		return
	}
	switch frame.Type {
	case "checkpoint_stream_begin":
		err = transfer.sink.Begin(transfer.ctx, frame.Schema)
	case "checkpoint_stream_chunk":
		index, _ := pluginprotocol.StreamCounter(frame.Index, false)
		err = transfer.sink.Append(transfer.ctx, index, raw)
	case "checkpoint_stream_end":
		err = transfer.sink.Complete(transfer.ctx, count)
	}
	if err != nil {
		p.expireCheckpoint(frame.RequestID, pending, err)
		return
	}
	if frame.Type == "checkpoint_stream_end" {
		p.mu.Lock()
		if p.pending[frame.RequestID] == pending {
			delete(p.pending, frame.RequestID)
			p.streamFinished[frame.RequestID] = transfer
			p.streamFinishedWatch[frame.SubscriptionID] = frame.RequestID
			p.mu.Unlock()
			pending.result <- requestResult{frame: pluginprotocol.Frame{Accepted: boolPointer(true)}}
		} else {
			p.mu.Unlock()
		}
	}
}

func (p *Process) rejectMalformedStream(payload []byte) {
	value, err := jsonvalue.Parse(payload, pluginprotocol.MaxPayloadBytes)
	if err != nil {
		// An unparseable bounded frame cannot establish a trustworthy owner.
		// Fail active snapshots closed rather than accepting a repaired suffix.
		p.mu.Lock()
		active := make(map[string]*pendingRequest)
		for id, request := range p.pending {
			if request.stream != nil {
				active[id] = request
			}
		}
		p.mu.Unlock()
		for id, request := range active {
			p.expireCheckpoint(id, request, errors.New("unparseable frame during checkpoint stream"))
		}
		return
	}
	kind, ok := value.Get("type")
	if !ok {
		return
	}
	name, ok := jsonvalue.ParseString(kind)
	if !ok || (name != "checkpoint_stream_begin" && name != "checkpoint_stream_chunk" && name != "checkpoint_stream_end") {
		return
	}
	request, ok := value.Get("request_id")
	if !ok {
		return
	}
	id, ok := jsonvalue.ParseString(request)
	if !ok {
		return
	}
	p.mu.Lock()
	pending := p.pending[id]
	finished := p.streamFinished[id]
	p.mu.Unlock()
	if pending != nil && pending.stream != nil {
		p.expireCheckpoint(id, pending, errors.New("malformed checkpoint stream frame"))
	} else if finished != nil {
		finished.sink.Abort(errors.New("malformed frame after checkpoint end"))
	}
}
