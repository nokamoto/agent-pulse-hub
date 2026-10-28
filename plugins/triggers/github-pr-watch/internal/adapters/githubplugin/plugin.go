package githubplugin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	randmath "math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

type Option func(*Plugin)

func WithByteBudgets(observation, history uint64) Option {
	return func(p *Plugin) { p.observationBudget = observation; p.historyBudget = history }
}

func WithWait(wait func(context.Context, time.Duration) bool) Option {
	return func(p *Plugin) {
		if wait != nil {
			p.wait = wait
		}
	}
}

type Plugin struct {
	logger            *slog.Logger
	credentials       application.Credentials
	github            application.GitHub
	host              string
	interval          time.Duration
	wait              func(context.Context, time.Duration) bool
	mu                sync.Mutex
	active            bool
	observationBudget uint64
	historyBudget     uint64
}

func New(logger *slog.Logger, credentials application.Credentials, github application.GitHub, host string, interval time.Duration, options ...Option) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	p := &Plugin{logger: logger, credentials: credentials, github: github, host: host, interval: interval, wait: waitFor, observationBudget: domain.DefaultByteBudget, historyBudget: domain.DefaultByteBudget}
	for _, option := range options {
		option(p)
	}
	return p
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type watch struct {
	validated          application.Validated
	ctx                context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	started            bool
	quiesced           bool
	armed              bool
	generation         string
	preparedGeneration string
	preparedSequence   uint64
	eventCounter       uint64
	correlations       map[string]uint64
	checkpointCancel   context.CancelFunc
	checkpointDone     chan struct{}
}
type incoming struct {
	frame pluginprotocol.Frame
	err   error
}
type validation struct {
	requestID, subscriptionID string
	validated                 application.Validated
	err                       error
}
type restoring struct {
	frame   pluginprotocol.Frame
	ctx     context.Context
	cancel  context.CancelFunc
	count   uint64
	objects []*jsonvalue.Value
	bytes   uint64
	ended   bool
}
type observation struct {
	subscriptionID string
	value          observationValue
	ack            chan struct{}
}
type observationValue struct {
	snapshot domain.Observation
	err      error
}

// Session serializes mutations, grants, notices and checkpoint snapshots. Source
// I/O runs separately and cannot mutate retained state or write protocol frames.
func (p *Plugin) Session(stream pluginprotocol.SessionServer) error {
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return errors.New("a Session is already active")
	}
	p.active = true
	p.mu.Unlock()
	ctx, cancel := context.WithCancel(stream.Context())
	queued := newQueuedSession(ctx, stream)
	stream = queued
	watches := map[string]*watch{}
	defer func() {
		cancel()
		for _, w := range watches {
			w.cancel()
			if w.checkpointCancel != nil {
				w.checkpointCancel()
			}
		}
		for _, w := range watches {
			if w.started {
				<-w.done
			}
		}
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
	}()
	if err := send(stream, pluginprotocol.Frame{Type: "ready", Role: "input", Capabilities: []string{pluginprotocol.RuntimeStateV2Capability, pluginprotocol.RewaitCapability, pluginprotocol.RuntimeStateStreamCapability}}); err != nil {
		return err
	}
	frames := make(chan incoming, 16)
	validations := make(chan validation, 128)
	observations := make(chan observation, 16)
	pending := map[string]*restoring{}
	defer func() {
		for _, r := range pending {
			r.cancel()
		}
	}()
	go receive(ctx, stream, frames)
	handle := func(message incoming) error {
		if message.err != nil {
			return message.err
		}
		return p.handle(ctx, stream, watches, pending, validations, observations, message.frame)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-queued.failed:
			return err
		case message := <-frames:
			if err := handle(message); err != nil {
				if errors.Is(err, io.EOF) || ctx.Err() != nil {
					return nil
				}
				return err
			}
		case result := <-validations:
			// Drain controls already received before publishing network completion.
			for draining := true; draining; {
				select {
				case message := <-frames:
					if err := handle(message); err != nil {
						return err
					}
				default:
					draining = false
				}
			}
			r := pending[result.subscriptionID]
			if r == nil || r.frame.RequestID != result.requestID {
				continue
			}
			if result.err == nil && r.ctx.Err() != nil {
				result.err = context.DeadlineExceeded
			}
			r.cancel()
			delete(pending, result.subscriptionID)
			if result.err != nil {
				p.diagnostic(result.subscriptionID, result.err)
				if err := send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: result.requestID, Accepted: boolean(false), Error: "GitHub PR watch validation failed; inspect plugin diagnostics"}); err != nil {
					return err
				}
				continue
			}
			watchCtx, watchCancel := context.WithCancel(ctx)
			w := &watch{validated: result.validated, ctx: watchCtx, cancel: watchCancel, done: make(chan struct{}), correlations: map[string]uint64{}}
			if err := send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: result.requestID, Accepted: boolean(true)}); err != nil {
				watchCancel()
				return err
			}
			watches[result.subscriptionID] = w
		case result := <-observations:
			for draining := true; draining; {
				select {
				case message := <-frames:
					if err := handle(message); err != nil {
						close(result.ack)
						return err
					}
				default:
					draining = false
				}
			}
			w := watches[result.subscriptionID]
			if w != nil && !w.quiesced {
				if result.value.err != nil {
					w.validated.Tracker.RefreshFailure = failureCode(result.value.err)
					p.diagnostic(result.subscriptionID, result.value.err)
				} else {
					if err := w.validated.Tracker.Reconcile(result.value.snapshot); err != nil {
						w.validated.Tracker.RefreshFailure = failureCode(err)
						p.diagnostic(result.subscriptionID, err)
					} else if err := p.emit(stream, result.subscriptionID, w); err != nil {
						close(result.ack)
						return err
					}
				}
			}
			close(result.ack)
		}
	}
}

func receive(ctx context.Context, stream pluginprotocol.SessionServer, frames chan<- incoming) {
	for {
		payload, err := stream.Recv()
		message := incoming{err: err}
		if err == nil {
			message.frame, err = pluginprotocol.ParsePayload(payload, pluginprotocol.DaemonToPlugin)
			message.err = err
		}
		select {
		case frames <- message:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}
func boolean(v bool) *bool { return &v }
func send(stream pluginprotocol.SessionServer, frame pluginprotocol.Frame) error {
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return err
	}
	if queued, ok := stream.(*queuedSession); ok && (frame.Type == "watch_result" || frame.Type == "resume_result") && frame.Accepted != nil && *frame.Accepted {
		return queued.sendBarrier(payload)
	}
	return stream.Send(payload)
}

func (p *Plugin) handle(ctx context.Context, stream pluginprotocol.SessionServer, watches map[string]*watch, pending map[string]*restoring, validations chan<- validation, observations chan<- observation, frame pluginprotocol.Frame) error {
	w := watches[frame.SubscriptionID]
	switch frame.Type {
	case "state_stream_watch":
		if w != nil || pending[frame.SubscriptionID] != nil || len(pending) >= 128 {
			return send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "watch already exists or validation capacity reached"})
		}
		count, err := strconv.ParseUint(frame.ChunkCount, 10, 64)
		if err != nil || (frame.Schema == "" && count != 0) || (frame.Schema != "" && count == 0) {
			return errors.New("invalid state stream begin")
		}
		validationCtx, validationCancel := context.WithTimeout(ctx, 10*time.Second)
		r := &restoring{frame: frame, ctx: validationCtx, cancel: validationCancel, count: count}
		pending[frame.SubscriptionID] = r
		go func() {
			select {
			case <-validationCtx.Done():
				select {
				case validations <- validation{frame.RequestID, frame.SubscriptionID, application.Validated{}, errors.New("state restoration deadline exceeded")}:
				case <-ctx.Done():
				}
			case <-ctx.Done():
			}
		}()
	case "state_stream_chunk":
		r := pending[frame.SubscriptionID]
		if r == nil || r.frame.RequestID != frame.RequestID || r.ended || r.ctx.Err() != nil {
			return errors.New("unexpected state stream chunk")
		}
		index, err := strconv.ParseUint(frame.Index, 10, 64)
		if err != nil || index != uint64(len(r.objects)) || index >= r.count || frame.Data == nil {
			return p.rejectRestore(stream, pending, frame.SubscriptionID, "invalid state stream order")
		}
		limit := p.restoreBudget()
		n := uint64(len(frame.Data.Marshal()))
		if r.bytes > limit || n > limit-r.bytes {
			return p.rejectRestore(stream, pending, frame.SubscriptionID, "state restoration byte budget exceeded")
		}
		r.bytes += n
		r.objects = append(r.objects, frame.Data)
	case "state_stream_end":
		r := pending[frame.SubscriptionID]
		if r == nil || r.frame.RequestID != frame.RequestID || r.ended {
			return errors.New("unexpected state stream end")
		}
		count, err := strconv.ParseUint(frame.ChunkCount, 10, 64)
		if err != nil || count != r.count || count != uint64(len(r.objects)) || r.ctx.Err() != nil {
			return p.rejectRestore(stream, pending, frame.SubscriptionID, "incomplete state restoration")
		}
		r.ended = true
		go func() {
			nonce := make([]byte, 16)
			_, err := rand.Read(nonce)
			var validated application.Validated
			if err == nil {
				validated, err = application.Validate(r.ctx, r.frame.WatchArgs, nil, p.host, nonce, p.credentials, p.github)
			}
			if err == nil {
				validated.Tracker.ObservationBudget = p.observationBudget
				validated.Tracker.HistoryBudget = p.historyBudget
				if r.frame.Schema != "" {
					validated.Tracker, err = domain.RestoreStream(validated.Config, r.frame.Schema, r.objects, p.observationBudget, p.historyBudget)
				}
			}
			select {
			case validations <- validation{frame.RequestID, frame.SubscriptionID, validated, err}:
			case <-ctx.Done():
			}
		}()
	case "watch":
		return send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "github-pr-watch requires runtime_state_v2, rewait_v1 and runtime_state_stream_v1"})
	case "activate_watch":
		if w == nil || w.quiesced || frame.Generation == "" {
			return nil
		}
		if w.generation == frame.Generation {
			return nil
		}
		if !w.started {
			w.generation = frame.Generation
			w.armed = true
			w.started = true
			go p.poll(frame.SubscriptionID, w, observations)
			return p.emit(stream, frame.SubscriptionID, w)
		}
		if frame.Generation != w.preparedGeneration || w.preparedGeneration == "" {
			p.diagnostic(frame.SubscriptionID, errors.New("stale_generation"))
			return nil
		}
		w.validated.Tracker.Resume(w.preparedSequence)
		w.preparedGeneration = ""
		w.generation = frame.Generation
		w.armed = true
		return p.emit(stream, frame.SubscriptionID, w)
	case "resume_watch":
		err := errors.New("watch_unavailable")
		var sequence uint64
		if w != nil && !w.quiesced && w.started && !w.armed && len(w.correlations) == 0 && w.preparedGeneration == "" && frame.Generation != w.generation {
			sequence, err = w.validated.Tracker.ValidatePosition(frame.ConfirmationPosition)
		}
		if err != nil {
			return send(stream, pluginprotocol.Frame{Type: "resume_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: err.Error()})
		}
		// Preparation does not mutate comparison progress; only its matching grant
		// can commit the position after the positive response has been sent.
		if err := send(stream, pluginprotocol.Frame{Type: "resume_result", RequestID: frame.RequestID, Accepted: boolean(true)}); err != nil {
			return err
		}
		w.preparedGeneration = frame.Generation
		w.preparedSequence = sequence
		w.validated.Tracker.Prepare(sequence)
	case "delivery_notice":
		if w == nil {
			return nil
		}
		sequence, exists := w.correlations[frame.EventID]
		if !exists {
			return nil
		}
		delete(w.correlations, frame.EventID)
		w.validated.Tracker.Notice(sequence, frame.Status)
	case "checkpoint":
		if w == nil {
			return send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "watch unavailable"})
		}
		if frame.Quiesce {
			w.quiesced = true
			w.preparedGeneration = ""
			w.validated.Tracker.CancelPreparation()
			w.cancel()
		}
		if w.checkpointCancel != nil {
			w.checkpointCancel()
		}
		if w.checkpointDone != nil {
			<-w.checkpointDone
		}
		checkpointCtx, checkpointCancel := context.WithTimeout(ctx, checkpointTimeout)
		w.checkpointCancel = checkpointCancel
		objects, err := w.validated.Tracker.StreamCheckpoint()
		if err != nil {
			checkpointCancel()
			return send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "checkpoint capacity exceeded"})
		}
		w.checkpointDone = make(chan struct{})
		go func() {
			defer close(w.checkpointDone)
			sendCheckpoint(checkpointCtx, checkpointCancel, stream.(*queuedSession), frame, objects)
		}()
	default:
		return errors.New("unexpected daemon protocol frame")
	}
	return nil
}

func (p *Plugin) restoreBudget() uint64 {
	a, b := p.observationBudget, p.historyBudget
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	a += b
	if math.MaxUint64-a < 32768 {
		return math.MaxUint64
	}
	return a + 32768
}

func (p *Plugin) rejectRestore(stream pluginprotocol.SessionServer, pending map[string]*restoring, id, reason string) error {
	r := pending[id]
	if r == nil {
		return errors.New("unexpected state stream")
	}
	r.cancel()
	delete(pending, id)
	return send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: r.frame.RequestID, Accepted: boolean(false), Error: reason})
}

func (p *Plugin) emit(stream pluginprotocol.SessionServer, id string, w *watch) error {
	if !w.armed || w.quiesced || w.preparedGeneration != "" {
		return nil
	}
	text, sequence, err := w.validated.Tracker.Context()
	if err != nil {
		p.diagnostic(id, err)
		return nil
	}
	if text == "" {
		return nil
	}
	if w.eventCounter == math.MaxUint64 {
		return errors.New("event_sequence_exhausted")
	}
	w.eventCounter++
	eventID := strconv.FormatUint(w.eventCounter, 10)
	w.armed = false
	w.correlations[eventID] = sequence
	return send(stream, pluginprotocol.Frame{Type: "state_event", SubscriptionID: id, EventID: eventID, Generation: w.generation, Context: text})
}

func (p *Plugin) diagnostic(id string, err error) {
	code := failureCode(err)
	var resource *domain.ResourceError
	if errors.As(err, &resource) {
		p.logger.Warn("github_pr_watch_failure", "registration_id", fmt.Sprintf("%.128s", id), "category", resource.Code, "option", resource.Option, "budget", resource.Budget, "observed_bytes", resource.Observed)
		return
	}
	p.logger.Warn("github_pr_watch_failure", "registration_id", fmt.Sprintf("%.128s", id), "category", code)
}

func failureCode(err error) string {
	code := "retrieval_failure"
	var failure *application.Failure
	var resource *domain.ResourceError
	if errors.As(err, &resource) {
		return resource.Code
	}
	if errors.As(err, &failure) {
		code = failure.Code
	} else {
		switch err.Error() {
		case "coverage_capacity_exceeded", "notification_capacity_exceeded", "item_metadata_exhausted", "notification_metadata_exhausted", "sequence_exhausted", "stale_generation", "credential_unavailable", "invalid restored state":
			code = err.Error()
		}
	}
	return code
}

func (p *Plugin) poll(id string, w *watch, observations chan<- observation) {
	defer close(w.done)
	var level uint
	for {
		roundCtx, cancel := context.WithTimeout(w.ctx, 300*time.Second)
		snapshot, err := p.github.Observe(roundCtx, w.validated.Config, w.validated.Token)
		cancel()
		ack := make(chan struct{})
		select {
		case observations <- observation{id, observationValue{snapshot, err}, ack}:
		case <-w.ctx.Done():
			return
		}
		select {
		case <-ack:
		case <-w.ctx.Done():
			return
		}
		delay := application.RetryDelay(p.interval, err, level, time.Now(), randmath.Float64())
		if err == nil {
			level = 0
		} else if level < 4 {
			level++
		}
		if !p.wait(w.ctx, delay) {
			return
		}
	}
}
