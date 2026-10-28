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
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-pr-watch/internal/domain"
)

type Option func(*Plugin)

//go:generate go run go.uber.org/mock/mockgen -source=../../application/watch.go -destination=github_mock_test.go -package=githubplugin

func WithWait(wait func(context.Context, time.Duration) bool) Option {
	return func(p *Plugin) {
		if wait != nil {
			p.wait = wait
		}
	}
}

type Plugin struct {
	logger      *slog.Logger
	credentials application.Credentials
	github      application.GitHub
	host        string
	interval    time.Duration
	wait        func(context.Context, time.Duration) bool
	mu          sync.Mutex
	active      bool
}

func New(logger *slog.Logger, credentials application.Credentials, github application.GitHub, host string, interval time.Duration, options ...Option) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	p := &Plugin{logger: logger, credentials: credentials, github: github, host: host, interval: interval, wait: waitFor}
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
type observation struct {
	subscriptionID string
	value          observationValue
	ack            chan struct{}
}
type observationValue struct {
	snapshot domain.Observation
	err      error
}

type cancellation struct {
	requestID, subscriptionID string
	watch                     *watch
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
	watches := map[string]*watch{}
	defer func() {
		cancel()
		for _, w := range watches {
			w.cancel()
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
	if err := send(stream, pluginprotocol.Frame{Type: "ready", Role: "input", Capabilities: []string{pluginprotocol.RuntimeStateV2Capability, pluginprotocol.RewaitCapability, pluginprotocol.CancelWatchCapability}}); err != nil {
		return err
	}
	frames := make(chan incoming, 16)
	validations := make(chan validation, 128)
	observations := make(chan observation, 16)
	cancellations := make(chan cancellation, 128)
	pending := map[string]bool{}
	go receive(ctx, stream, frames)
	handle := func(message incoming) error {
		if message.err != nil {
			return message.err
		}
		frame := message.frame
		if frame.Type == "cancel_watch" {
			delete(pending, frame.SubscriptionID)
			w := watches[frame.SubscriptionID]
			if w == nil {
				return send(stream, pluginprotocol.Frame{Type: "cancel_result", RequestID: frame.RequestID, Accepted: boolean(true)})
			}
			w.quiesced = true
			w.preparedGeneration = ""
			w.validated.Tracker.CancelPreparation()
			w.cancel()
			started := w.started
			go func() {
				if started {
					<-w.done
				}
				select {
				case cancellations <- cancellation{frame.RequestID, frame.SubscriptionID, w}:
				case <-ctx.Done():
				}
			}()
			return nil
		}
		return p.handle(ctx, stream, watches, pending, validations, observations, message.frame)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case message := <-frames:
			if err := handle(message); err != nil {
				if errors.Is(err, io.EOF) || ctx.Err() != nil {
					return nil
				}
				return err
			}
		case result := <-cancellations:
			if watches[result.subscriptionID] == result.watch {
				delete(watches, result.subscriptionID)
			}
			if err := send(stream, pluginprotocol.Frame{Type: "cancel_result", RequestID: result.requestID, Accepted: boolean(true)}); err != nil {
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
			if !pending[result.subscriptionID] {
				continue
			}
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
					p.diagnostic(result.subscriptionID, result.value.err)
				} else {
					if err := w.validated.Tracker.Reconcile(result.value.snapshot); err != nil {
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
	return stream.Send(payload)
}

func (p *Plugin) handle(ctx context.Context, stream pluginprotocol.SessionServer, watches map[string]*watch, pending map[string]bool, validations chan<- validation, observations chan<- observation, frame pluginprotocol.Frame) error {
	w := watches[frame.SubscriptionID]
	switch frame.Type {
	case "state_watch":
		if w != nil || pending[frame.SubscriptionID] || len(pending) >= 128 {
			return send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "watch already exists or validation capacity reached"})
		}
		pending[frame.SubscriptionID] = true
		go func() {
			validationCtx, cancel := context.WithTimeout(ctx, 9*time.Second)
			defer cancel()
			nonce := make([]byte, 16)
			_, err := rand.Read(nonce)
			var validated application.Validated
			if err == nil {
				validated, err = application.Validate(validationCtx, frame.WatchArgs, frame.State, p.host, nonce, p.credentials, p.github)
			}
			select {
			case validations <- validation{frame.RequestID, frame.SubscriptionID, validated, err}:
			case <-ctx.Done():
			}
		}()
	case "watch":
		return send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "github-pr-watch requires runtime_state_v2 and rewait_v1"})
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
		state, err := w.validated.Tracker.Checkpoint()
		if err != nil {
			return send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolean(false), Error: "checkpoint capacity exceeded"})
		}
		return send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolean(true), State: state})
	default:
		return errors.New("unexpected daemon protocol frame")
	}
	return nil
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
	code := "retrieval_failure"
	var failure *application.Failure
	if errors.As(err, &failure) {
		code = failure.Code
	} else {
		switch err.Error() {
		case "coverage_capacity_exceeded", "notification_capacity_exceeded", "sequence_exhausted", "stale_generation", "credential_unavailable", "invalid restored state":
			code = err.Error()
		}
	}
	p.logger.Warn("github_pr_watch_failure", "registration_id", fmt.Sprintf("%.128s", id), "category", code)
}

func (p *Plugin) poll(id string, w *watch, observations chan<- observation) {
	defer close(w.done)
	var level uint
	for {
		roundCtx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
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
