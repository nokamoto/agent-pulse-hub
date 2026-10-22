package githubplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/application"
	"github.com/nokamoto/agent-pulse-hub/plugins/triggers/github-review-request/internal/domain"
)

const coverageNotice = "Search covers at most 4,000 GitHub-selected repositories; coverage beyond that limit cannot be detected or guaranteed."

const maxConcurrentValidations = 1_024

type Plugin struct {
	logger      *slog.Logger
	credentials application.Credentials
	github      application.GitHub

	mu     sync.Mutex
	active bool
}

type watch struct {
	subscriptionID string
	validated      application.ValidatedWatch
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	quiesced       bool
	eventCounter   uint64
	outstanding    string
	correlations   map[string]domain.Attempt
}

type incoming struct {
	frame pluginprotocol.Frame
	err   error
}

type observation struct {
	subscriptionID string
	matches        map[uint64]string
	err            error
	ack            chan struct{}
}

type validationResult struct {
	requestID      string
	subscriptionID string
	validated      application.ValidatedWatch
	err            error
}

func New(logger *slog.Logger, credentials application.Credentials, github application.GitHub) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	return &Plugin{logger: logger, credentials: credentials, github: github}
}

func (p *Plugin) Session(stream pluginprotocol.SessionServer) error {
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return errors.New("an EventPlugin Session is already active")
	}
	p.active = true
	p.mu.Unlock()

	ctx, cancel := context.WithCancel(stream.Context())
	defer func() {
		cancel()
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
	}()
	if err := p.send(stream, pluginprotocol.Frame{Type: "ready", Role: "input", Capabilities: []string{pluginprotocol.RuntimeStateCapability}}); err != nil {
		return fmt.Errorf("send readiness: %w", err)
	}

	incomingFrames := make(chan incoming, 16)
	observations := make(chan observation, 16)
	validations := make(chan validationResult, maxConcurrentValidations)
	go receive(ctx, stream, incomingFrames)
	watches := make(map[string]*watch)
	pending := make(map[string]struct{})
	defer stopWatches(watches)

	for {
		select {
		case <-ctx.Done():
			return nil
		case message := <-incomingFrames:
			if message.err != nil {
				if errors.Is(message.err, io.EOF) || ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("receive daemon message: %w", message.err)
			}
			if err := p.handleFrame(ctx, stream, watches, pending, observations, validations, message.frame); err != nil {
				return err
			}
		case result := <-validations:
			// Process already-received protocol frames before completing a validation.
			// This keeps duplicate detection and checkpoint handling on the session
			// coordinator while network and credential work stays off it.
		priorityValidationDrain:
			select {
			case message := <-incomingFrames:
				if message.err != nil {
					if errors.Is(message.err, io.EOF) || ctx.Err() != nil {
						return nil
					}
					return fmt.Errorf("receive daemon message: %w", message.err)
				}
				if err := p.handleFrame(ctx, stream, watches, pending, observations, validations, message.frame); err != nil {
					return err
				}
				goto priorityValidationDrain
			default:
			}
			if err := p.applyValidation(ctx, stream, watches, pending, observations, result); err != nil {
				return err
			}
		case result := <-observations:
			// Give already-received protocol frames priority over a completed HTTP
			// observation so a quiescing checkpoint fences late results.
		priorityDrain:
			select {
			case message := <-incomingFrames:
				if message.err != nil {
					if errors.Is(message.err, io.EOF) || ctx.Err() != nil {
						close(result.ack)
						return nil
					}
					close(result.ack)
					return fmt.Errorf("receive daemon message: %w", message.err)
				}
				if err := p.handleFrame(ctx, stream, watches, pending, observations, validations, message.frame); err != nil {
					close(result.ack)
					return err
				}
				goto priorityDrain
			default:
			}
			if err := p.applyObservation(stream, watches, result); err != nil {
				close(result.ack)
				return err
			}
			close(result.ack)
		}
	}
}

func receive(ctx context.Context, stream pluginprotocol.SessionServer, output chan<- incoming) {
	for {
		payload, err := stream.Recv()
		if err != nil {
			select {
			case output <- incoming{err: err}:
			case <-ctx.Done():
			}
			return
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.DaemonToPlugin)
		if err != nil {
			select {
			case output <- incoming{err: errors.New("daemon sent an invalid protocol frame")}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case output <- incoming{frame: frame}:
		case <-ctx.Done():
			return
		}
	}
}

func (p *Plugin) handleFrame(ctx context.Context, stream pluginprotocol.SessionServer, watches map[string]*watch, pending map[string]struct{}, observations chan<- observation, validations chan<- validationResult, frame pluginprotocol.Frame) error {
	switch frame.Type {
	case "state_watch":
		_, active := watches[frame.SubscriptionID]
		_, validating := pending[frame.SubscriptionID]
		if active || validating {
			return p.send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolPointer(false), Error: "github-review-request watch is already active"})
		}
		if len(pending) >= maxConcurrentValidations {
			p.logger.Error("github_review_request_watch_rejected", "registration_id", frame.SubscriptionID, "reason", "validation capacity reached", "category", application.FailureCapacity)
			return p.send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolPointer(false), Error: "GitHub review request watch validation capacity is full; retry registration"})
		}
		validationCtx, cancelValidation := context.WithTimeout(ctx, application.WatchValidationTimeout)
		pending[frame.SubscriptionID] = struct{}{}
		go func(frame pluginprotocol.Frame) {
			defer cancelValidation()
			validated, err := application.ValidateWatch(validationCtx, frame.WatchArgs, frame.State, p.credentials, p.github)
			result := validationResult{requestID: frame.RequestID, subscriptionID: frame.SubscriptionID, validated: validated, err: err}
			select {
			case validations <- result:
			case <-ctx.Done():
			}
		}(frame)
		return nil
	case "watch":
		return p.send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: frame.RequestID, Accepted: boolPointer(false), Error: "github-review-request requires runtime-state protocol support"})
	case "delivery_notice":
		current := watches[frame.SubscriptionID]
		if current == nil {
			return nil
		}
		attempt, exists := current.correlations[frame.EventID]
		if !exists {
			return nil
		}
		delete(current.correlations, frame.EventID)
		if current.outstanding == frame.EventID {
			current.outstanding = ""
		}
		current.validated.Tracker.ApplyNotice(attempt.IssueID, attempt.Generation, frame.Status)
		if current.quiesced {
			return nil
		}
		return p.emitNext(stream, current)
	case "checkpoint":
		current := watches[frame.SubscriptionID]
		if current == nil {
			return p.send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolPointer(false), Error: "GitHub review request watch is unavailable"})
		}
		if frame.Quiesce && !current.quiesced {
			current.quiesced = true
			current.cancel()
		}
		state, err := current.validated.Tracker.Checkpoint()
		if err != nil {
			return p.send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolPointer(false), Error: "GitHub review request checkpoint could not be encoded"})
		}
		return p.send(stream, pluginprotocol.Frame{Type: "checkpoint_result", RequestID: frame.RequestID, Accepted: boolPointer(true), State: state})
	default:
		return errors.New("unexpected daemon protocol frame")
	}
}

func (p *Plugin) applyValidation(ctx context.Context, stream pluginprotocol.SessionServer, watches map[string]*watch, pending map[string]struct{}, observations chan<- observation, result validationResult) error {
	if _, exists := pending[result.subscriptionID]; !exists {
		return nil
	}
	delete(pending, result.subscriptionID)
	if ctx.Err() != nil {
		return nil
	}
	if result.err != nil {
		attrs := []any{"registration_id", result.subscriptionID}
		var validationFailure *application.WatchValidationError
		if errors.As(result.err, &validationFailure) {
			attrs = append(attrs, "reason", validationFailure.Reason)
			if validationFailure.Host != "" {
				attrs = append(attrs, "host", validationFailure.Host)
			}
			if validationFailure.Category != "" {
				attrs = append(attrs, "category", validationFailure.Category)
			}
			if validationFailure.StatusCode != 0 {
				attrs = append(attrs, "http_status", validationFailure.StatusCode)
			}
		} else {
			attrs = append(attrs, "reason", "watch validation failed")
		}
		p.logger.Error("github_review_request_watch_rejected", attrs...)
		return p.send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: result.requestID, Accepted: boolPointer(false), Error: "GitHub review request watch validation failed; inspect plugin diagnostics"})
	}
	watchCtx, cancel := context.WithCancel(ctx)
	current := &watch{
		subscriptionID: result.subscriptionID,
		validated:      result.validated,
		ctx:            watchCtx,
		cancel:         cancel,
		done:           make(chan struct{}),
		correlations:   make(map[string]domain.Attempt),
	}
	if err := p.send(stream, pluginprotocol.Frame{Type: "watch_result", RequestID: result.requestID, Accepted: boolPointer(true)}); err != nil {
		cancel()
		return fmt.Errorf("send watch result: %w", err)
	}
	watches[result.subscriptionID] = current
	p.logger.Warn("github_review_request_coverage_limit", "registration_id", result.subscriptionID, "host", result.validated.Config.Host, "notice", coverageNotice)
	go p.poll(current, observations)
	return nil
}

func (p *Plugin) poll(current *watch, results chan<- observation) {
	defer close(current.done)
	var backoffLevel uint
	for {
		observationCtx, cancel := context.WithTimeout(current.ctx, 120*time.Second)
		matches, err := p.github.Observe(observationCtx, current.validated.Config, current.validated.Token)
		cancel()
		ack := make(chan struct{})
		select {
		case results <- observation{subscriptionID: current.subscriptionID, matches: matches, err: err, ack: ack}:
		case <-current.ctx.Done():
			return
		}
		select {
		case <-ack:
		case <-current.ctx.Done():
			return
		}
		if current.ctx.Err() != nil {
			return
		}
		failure, _ := err.(*application.Failure)
		if failure == nil {
			if err == nil {
				backoffLevel = 0
			} else {
				failure = &application.Failure{Kind: application.FailureNetwork}
			}
		}
		delay := application.RetryDelay(current.validated.Config.PollInterval, failure, backoffLevel)
		if failure != nil && (failure.Kind == application.FailureNetwork || failure.Kind == application.FailureService || failure.Kind == application.FailureRateLimit) {
			if backoffLevel < 9 {
				backoffLevel++
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-current.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func (p *Plugin) applyObservation(stream pluginprotocol.SessionServer, watches map[string]*watch, result observation) error {
	current := watches[result.subscriptionID]
	if current == nil || current.quiesced || current.ctx.Err() != nil {
		return nil
	}
	if result.err != nil {
		failure, ok := result.err.(*application.Failure)
		if !ok {
			failure = &application.Failure{Kind: application.FailureNetwork}
		}
		p.logger.Warn("github_review_request_observation_failed",
			"registration_id", current.subscriptionID,
			"host", current.validated.Config.Host,
			"category", failure.Kind,
			"http_status", failure.StatusCode,
			"reason", safeReason(failure),
		)
		return nil
	}
	if err := current.validated.Tracker.Reconcile(result.matches); err != nil {
		p.logger.Error("github_review_request_observation_rejected", "registration_id", current.subscriptionID, "host", current.validated.Config.Host, "category", application.FailureCapacity)
		return nil
	}
	return p.emitNext(stream, current)
}

func (p *Plugin) emitNext(stream pluginprotocol.SessionServer, current *watch) error {
	if current.quiesced || current.outstanding != "" || current.ctx.Err() != nil {
		return nil
	}
	if current.eventCounter == math.MaxUint64 {
		if _, exists := current.validated.Tracker.NextAttempt(); exists {
			return errors.New("GitHub review request event counter exhausted")
		}
		return nil
	}
	attempt, exists := current.validated.Tracker.NextAttempt()
	if !exists {
		return nil
	}
	current.eventCounter++
	eventID := fmt.Sprintf("%d", current.eventCounter)
	current.outstanding = eventID
	current.correlations[eventID] = attempt
	if err := p.send(stream, pluginprotocol.Frame{
		Type: "state_event", SubscriptionID: current.subscriptionID,
		EventID: eventID, Context: attempt.URL,
	}); err != nil {
		return fmt.Errorf("send GitHub review request event: %w", err)
	}
	return nil
}

func (p *Plugin) send(stream pluginprotocol.SessionServer, frame pluginprotocol.Frame) error {
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return err
	}
	return stream.Send(payload)
}

func stopWatches(watches map[string]*watch) {
	for _, current := range watches {
		current.cancel()
	}
	for _, current := range watches {
		<-current.done
	}
}

func safeReason(failure *application.Failure) string {
	if failure.StatusCode != 0 {
		return fmt.Sprintf("HTTP %d", failure.StatusCode)
	}
	if failure.Reason != "" {
		return failure.Reason
	}
	return string(failure.Kind)
}

func boolPointer(value bool) *bool { return &value }
