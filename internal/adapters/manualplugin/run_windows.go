//go:build windows

package manualplugin

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	"golang.org/x/sys/windows"
)

const pollInterval = 100 * time.Millisecond

type runner struct {
	input      *bufio.Reader
	output     io.Writer
	diagnostic io.Writer
	files      triggerFiles

	outMu     sync.Mutex
	watchMu   sync.Mutex
	watches   map[string]*watcher
	stopping  bool
	rootCtx   context.Context
	stopAll   context.CancelFunc
	failureCh chan error
}

type watcher struct {
	subscriptionID string
	target         watchTarget
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
}

type readResult struct {
	frame pluginprotocol.Frame
	err   error
}

func Run(input io.Reader, output, diagnostic io.Writer) error {
	if output == nil {
		return errors.New("plugin stdout is required")
	}
	if diagnostic == nil {
		diagnostic = io.Discard
	}
	root, cancel := context.WithCancel(context.Background())
	p := &runner{
		input:      bufio.NewReader(input),
		output:     output,
		diagnostic: diagnostic,
		files:      localTriggerFiles{},
		watches:    make(map[string]*watcher),
		rootCtx:    root,
		stopAll:    cancel,
		failureCh:  make(chan error, 1),
	}
	if err := p.write(pluginprotocol.Frame{Type: "ready"}); err != nil {
		cancel()
		return fmt.Errorf("write plugin readiness: %w", err)
	}
	reads := make(chan readResult, 1)
	go p.readFrames(reads)
	for {
		select {
		case result := <-reads:
			if result.err != nil {
				p.stop()
				if errors.Is(result.err, io.EOF) {
					return nil
				}
				p.log("invalid daemon input: " + safeDiagnostic(result.err.Error()))
				return result.err
			}
			switch result.frame.Type {
			case "watch":
				if err := p.acceptWatch(result.frame); err != nil {
					p.stop()
					p.log("write watch result: " + safeDiagnostic(err.Error()))
					return err
				}
			case "shutdown":
				p.stop()
				return nil
			default:
				p.stop()
				err := errors.New("unexpected daemon frame")
				p.log(err.Error())
				return err
			}
		case err := <-p.failureCh:
			p.stop()
			p.log("plugin output failed: " + safeDiagnostic(err.Error()))
			return err
		case <-p.rootCtx.Done():
			p.stop()
			return nil
		}
	}
}

func (p *runner) readFrames(results chan<- readResult) {
	for {
		line, err := protocol.ReadFrame(p.input, protocol.MaxFrameBytes)
		if err != nil {
			select {
			case results <- readResult{err: err}:
			case <-p.rootCtx.Done():
			}
			return
		}
		value, err := protocol.ParseFrame(line)
		if err != nil {
			select {
			case results <- readResult{err: fmt.Errorf("malformed daemon frame: %w", err)}:
			case <-p.rootCtx.Done():
			}
			return
		}
		frame, err := pluginprotocol.Decode(value, pluginprotocol.DaemonToPlugin)
		if err != nil {
			select {
			case results <- readResult{err: fmt.Errorf("invalid daemon frame: %w", err)}:
			case <-p.rootCtx.Done():
			}
			return
		}
		select {
		case results <- readResult{frame: frame}:
		case <-p.rootCtx.Done():
			return
		}
	}
}

func (p *runner) acceptWatch(frame pluginprotocol.Frame) error {
	target, err := watchTargetFromArgumentsWithFiles(frame.WatchArgs, p.files)
	if err != nil {
		return p.write(pluginprotocol.Frame{
			Type:      "watch_result",
			RequestID: frame.RequestID,
			Accepted:  boolPointer(false),
			Error:     "trigger_file watch arguments are invalid",
		})
	}
	ctx, cancel := context.WithCancel(p.rootCtx)
	watch := &watcher{subscriptionID: frame.SubscriptionID, target: target, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	p.watchMu.Lock()
	if p.stopping {
		p.watchMu.Unlock()
		cancel()
		return errors.New("plugin is shutting down")
	}
	conflict := false
	for _, existing := range p.watches {
		if existing.subscriptionID == frame.SubscriptionID || sameTarget(existing.target.key, target.key) {
			conflict = true
			break
		}
	}
	if !conflict {
		p.watches[frame.SubscriptionID] = watch
	}
	p.watchMu.Unlock()
	if conflict {
		cancel()
		return p.write(pluginprotocol.Frame{
			Type:      "watch_result",
			RequestID: frame.RequestID,
			Accepted:  boolPointer(false),
			Error:     "trigger_file is already watched",
		})
	}
	if err := p.write(pluginprotocol.Frame{
		Type:      "watch_result",
		RequestID: frame.RequestID,
		Accepted:  boolPointer(true),
	}); err != nil {
		p.removeWatch(watch)
		cancel()
		return err
	}
	go p.runWatch(watch)
	return nil
}

func (p *runner) runWatch(watch *watcher) {
	defer close(watch.done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-watch.ctx.Done():
			return
		case <-ticker.C:
			contextText, claimedPath, err := consumeTriggerWithFiles(watch.target, p.files)
			if err != nil {
				p.log("claimed trigger failed at " + claimedPath + ": " + safeDiagnostic(err.Error()))
				continue
			}
			if contextText == "" {
				continue
			}
			if err := p.write(pluginprotocol.Frame{Type: "event", SubscriptionID: watch.subscriptionID, Context: contextText}); err != nil {
				select {
				case p.failureCh <- err:
				default:
				}
				return
			}
		}
	}
}

func consumeTriggerWithFiles(target watchTarget, files triggerFiles) (string, string, error) {
	claimPath, err := files.Claim(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return "", "", nil
		}
		return "", target.path, err
	}
	file, err := files.Open(claimPath)
	if err != nil {
		return "", claimPath, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, pluginprotocol.MaxContextBytes+1))
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

func claimFile(target watchTarget) (string, error) {
	for attempt := 0; attempt < 4; attempt++ {
		id, err := randomID()
		if err != nil {
			return "", err
		}
		claimPath := filepath.Join(target.directory, ".agent-pulse-hub-claim-"+id)
		from, err := windows.UTF16PtrFromString(target.path)
		if err != nil {
			return "", err
		}
		to, err := windows.UTF16PtrFromString(claimPath)
		if err != nil {
			return "", err
		}
		if err := windows.MoveFile(from, to); err == nil {
			return claimPath, nil
		} else if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		} else {
			return "", err
		}
	}
	return "", errors.New("could not create a unique claimed path")
}

func watchTargetFromArgumentsWithFiles(arguments *jsonvalue.Value, files triggerFiles) (watchTarget, error) {
	if err := protocol.ValidateFields(arguments, []string{"trigger_file"}, "trigger_file"); err != nil {
		return watchTarget{}, err
	}
	path, ok := protocol.StringField(arguments, "trigger_file")
	if !ok {
		return watchTarget{}, errors.New("trigger_file must be a string")
	}
	return parseWatchTargetWithFiles(path, files)
}

func (p *runner) write(frame pluginprotocol.Frame) error {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	return pluginprotocol.Write(p.output, frame)
}

func (p *runner) removeWatch(watch *watcher) {
	p.watchMu.Lock()
	if p.watches[watch.subscriptionID] == watch {
		delete(p.watches, watch.subscriptionID)
	}
	p.watchMu.Unlock()
}

func (p *runner) stop() {
	p.watchMu.Lock()
	if p.stopping {
		p.watchMu.Unlock()
		return
	}
	p.stopping = true
	p.stopAll()
	watches := make([]*watcher, 0, len(p.watches))
	for _, watch := range p.watches {
		watches = append(watches, watch)
	}
	p.watchMu.Unlock()
	for _, watch := range watches {
		select {
		case <-watch.done:
		case <-time.After(5 * time.Second):
		}
	}
}

func (p *runner) log(message string) {
	_, _ = fmt.Fprintln(p.diagnostic, safeDiagnostic(message))
}

func safeDiagnostic(message string) string {
	var output strings.Builder
	for _, character := range message {
		if unicode.IsControl(character) {
			character = ' '
		}
		if output.Len()+len(string(character)) > 1_024 {
			break
		}
		output.WriteRune(character)
	}
	return output.String()
}

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	data[6] = data[6]&0x0f | 0x40
	data[8] = data[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], data[0:4])
	hex.Encode(encoded[9:13], data[4:6])
	hex.Encode(encoded[14:18], data[6:8])
	hex.Encode(encoded[19:23], data[8:10])
	hex.Encode(encoded[24:36], data[10:16])
	encoded[8], encoded[13], encoded[18], encoded[23] = '-', '-', '-', '-'
	return string(encoded), nil
}

func boolPointer(value bool) *bool { return &value }
