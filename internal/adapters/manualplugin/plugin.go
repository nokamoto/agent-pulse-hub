package manualplugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	pollInterval          = 100 * time.Millisecond
	maxDiagnosticBytes    = 1_024
	maxTriggerReadBytes   = pluginprotocol.MaxContextBytes + 1
	claimFileNamePrefix   = ".agent-pulse-hub-claim-"
	writeProbeNamePattern = ".agent-pulse-hub-write-check-*"
)

type temporaryFile interface {
	Name() string
	Close() error
}

type triggerFiles interface {
	Stat(string) (os.FileInfo, error)
	Lstat(string) (os.FileInfo, error)
	CreateTemp(string, string) (temporaryFile, error)
	Open(string) (io.ReadCloser, error)
	Rename(string, string) error
	Remove(string) error
}

type localTriggerFiles struct{}

func (localTriggerFiles) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (localTriggerFiles) Lstat(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func (localTriggerFiles) CreateTemp(directory, pattern string) (temporaryFile, error) {
	return os.CreateTemp(directory, pattern)
}

func (localTriggerFiles) Open(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func (localTriggerFiles) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (localTriggerFiles) Remove(path string) error {
	return os.Remove(path)
}

type watchTarget struct {
	path      string
	directory string
}

type watcher struct {
	subscriptionID string
	target         watchTarget
	cancel         context.CancelFunc
	done           chan struct{}
}

// Plugin implements the manual trigger-file profile over the public
// language-neutral Session protocol.
type Plugin struct {
	logger *slog.Logger
	files  triggerFiles

	mu      sync.Mutex
	active  bool
	watches map[string]*watcher
	paths   map[string]string
}

func New(logger *slog.Logger) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	return newWithFiles(logger, localTriggerFiles{})
}

func newWithFiles(logger *slog.Logger, files triggerFiles) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	return &Plugin{
		logger:  logger,
		files:   files,
		watches: make(map[string]*watcher),
		paths:   make(map[string]string),
	}
}

func (p *Plugin) Session(stream pluginprotocol.SessionServer) error {
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return status.Error(codes.AlreadyExists, "an EventPlugin Session is already active")
	}
	p.active = true
	p.mu.Unlock()

	ctx, cancel := context.WithCancel(stream.Context())
	defer func() {
		cancel()
		p.stopWatches()
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
	}()

	if err := p.send(stream, pluginprotocol.Frame{Type: "ready"}); err != nil {
		return fmt.Errorf("send readiness: %w", err)
	}
	for {
		payload, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive daemon message: %w", err)
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.DaemonToPlugin)
		if err != nil {
			p.logger.Error("plugin_input_rejected", "reason", safeDiagnostic(err.Error()))
			return err
		}
		if frame.Type != "watch" {
			err := errors.New("unexpected daemon message")
			p.logger.Error("plugin_input_rejected", "reason", safeDiagnostic(err.Error()))
			return err
		}
		if err := p.acceptWatch(ctx, stream, frame); err != nil {
			return fmt.Errorf("accept watch: %w", err)
		}
	}
}

func (p *Plugin) acceptWatch(ctx context.Context, stream pluginprotocol.SessionServer, frame pluginprotocol.Frame) error {
	target, err := watchTargetFromArguments(frame.WatchArgs, p.files)
	if err != nil {
		return p.send(stream, pluginprotocol.Frame{
			Type: "watch_result", RequestID: frame.RequestID, Accepted: boolPointer(false),
			Error: "trigger_file watch arguments are invalid",
		})
	}

	p.mu.Lock()
	_, duplicateID := p.watches[frame.SubscriptionID]
	_, duplicatePath := p.paths[target.path]
	if !p.active || duplicateID || duplicatePath {
		p.mu.Unlock()
		return p.send(stream, pluginprotocol.Frame{
			Type: "watch_result", RequestID: frame.RequestID, Accepted: boolPointer(false),
			Error: "trigger_file is already watched",
		})
	}
	watchCtx, cancel := context.WithCancel(ctx)
	watch := &watcher{
		subscriptionID: frame.SubscriptionID,
		target:         target,
		cancel:         cancel,
		done:           make(chan struct{}),
	}
	p.watches[frame.SubscriptionID] = watch
	p.paths[target.path] = frame.SubscriptionID
	p.mu.Unlock()

	if err := p.send(stream, pluginprotocol.Frame{
		Type: "watch_result", RequestID: frame.RequestID, Accepted: boolPointer(true),
	}); err != nil {
		cancel()
		p.removeWatch(watch)
		return err
	}
	go p.runWatch(watchCtx, stream, watch)
	return nil
}

func (p *Plugin) runWatch(ctx context.Context, stream pluginprotocol.SessionServer, watch *watcher) {
	defer close(watch.done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			contextText, claimedPath, err := consumeTriggerWithFiles(watch.target, p.files)
			if err != nil {
				p.logger.Warn("manual_plugin_trigger_rejected",
					"subscription_id", watch.subscriptionID,
					"claimed_path", claimedPath,
					"reason", safeDiagnostic(err.Error()))
				continue
			}
			if contextText == "" {
				continue
			}
			if err := p.send(stream, pluginprotocol.Frame{
				Type: "event", SubscriptionID: watch.subscriptionID, Context: contextText,
			}); err != nil {
				p.logger.Warn("manual_plugin_event_send_failed",
					"subscription_id", watch.subscriptionID,
					"reason", safeDiagnostic(err.Error()))
				return
			}
		}
	}
}

func (p *Plugin) send(stream pluginprotocol.SessionServer, frame pluginprotocol.Frame) error {
	payload, err := pluginprotocol.Encode(frame)
	if err != nil {
		return err
	}
	return stream.Send(payload)
}

func (p *Plugin) stopWatches() {
	p.mu.Lock()
	watches := make([]*watcher, 0, len(p.watches))
	for _, watch := range p.watches {
		watches = append(watches, watch)
		watch.cancel()
	}
	p.watches = make(map[string]*watcher)
	p.paths = make(map[string]string)
	p.mu.Unlock()
	for _, watch := range watches {
		<-watch.done
	}
}

func (p *Plugin) removeWatch(watch *watcher) {
	p.mu.Lock()
	if p.watches[watch.subscriptionID] == watch {
		delete(p.watches, watch.subscriptionID)
		delete(p.paths, watch.target.path)
	}
	p.mu.Unlock()
}

func watchTargetFromArguments(arguments *jsonvalue.Value, files triggerFiles) (watchTarget, error) {
	if err := protocol.ValidateFields(arguments, []string{"trigger_file"}, "trigger_file"); err != nil {
		return watchTarget{}, err
	}
	path, ok := protocol.StringField(arguments, "trigger_file")
	if !ok {
		return watchTarget{}, errors.New("trigger_file must be a string")
	}
	return parseWatchTarget(path, files)
}

func parseWatchTarget(input string, files triggerFiles) (watchTarget, error) {
	if !filepath.IsAbs(input) {
		return watchTarget{}, errors.New("trigger_file must be an absolute path")
	}
	path := filepath.Clean(input)
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return watchTarget{}, errors.New("trigger_file must name a file")
	}
	directory := filepath.Dir(path)
	info, err := files.Stat(directory)
	if err != nil || !info.IsDir() {
		return watchTarget{}, errors.New("trigger_file parent must be an existing directory")
	}
	if _, err := files.Lstat(path); err == nil {
		return watchTarget{}, errors.New("trigger_file must not already exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return watchTarget{}, errors.New("trigger_file path is unavailable")
	}
	probe, err := files.CreateTemp(directory, writeProbeNamePattern)
	if err != nil {
		return watchTarget{}, errors.New("trigger_file parent must be writable")
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := files.Remove(probePath)
	if closeErr != nil || removeErr != nil {
		return watchTarget{}, errors.New("trigger_file parent write check failed")
	}
	return watchTarget{path: path, directory: directory}, nil
}

func consumeTriggerWithFiles(target watchTarget, files triggerFiles) (string, string, error) {
	claimPath, err := claimFile(target, files)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", nil
		}
		return "", target.path, err
	}
	file, err := files.Open(claimPath)
	if err != nil {
		return "", claimPath, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxTriggerReadBytes))
	closeErr := file.Close()
	if readErr != nil {
		return "", claimPath, readErr
	}
	if closeErr != nil {
		return "", claimPath, closeErr
	}
	if len(data) == 0 || len(data) > pluginprotocol.MaxContextBytes {
		return "", claimPath, fmt.Errorf("context must contain 1 to %d bytes", pluginprotocol.MaxContextBytes)
	}
	if !utf8.Valid(data) {
		return "", claimPath, errors.New("context is not valid UTF-8")
	}
	if err := files.Remove(claimPath); err != nil {
		return "", claimPath, err
	}
	return string(data), claimPath, nil
}

func claimFile(target watchTarget, files triggerFiles) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		id, err := randomID()
		if err != nil {
			return "", err
		}
		claimPath := filepath.Join(target.directory, claimFileNamePrefix+id)
		if err := files.Rename(target.path, claimPath); err == nil {
			return claimPath, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not create a unique claimed path")
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], raw[0:4])
	hex.Encode(encoded[9:13], raw[4:6])
	hex.Encode(encoded[14:18], raw[6:8])
	hex.Encode(encoded[19:23], raw[8:10])
	hex.Encode(encoded[24:36], raw[10:16])
	encoded[8], encoded[13], encoded[18], encoded[23] = '-', '-', '-', '-'
	return string(encoded), nil
}

func safeDiagnostic(message string) string {
	var output strings.Builder
	for _, character := range message {
		if unicode.IsControl(character) {
			character = ' '
		}
		if output.Len()+utf8.RuneLen(character) > maxDiagnosticBytes {
			break
		}
		output.WriteRune(character)
	}
	return output.String()
}

func boolPointer(value bool) *bool { return &value }
