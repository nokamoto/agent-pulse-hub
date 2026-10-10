package manualplugin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/jsonstrict"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

const pollInterval = 250 * time.Millisecond

type watchArgs struct {
	TriggerFile string `json:"trigger_file"`
}

type watch struct {
	subscriptionID string
	path           string
	pathKey        string
}

type Server struct {
	inputSource io.Reader
	input       *bufio.Reader
	output      *bufio.Writer
	stderr      io.Writer
	watches     map[string]watch
	paths       map[string]struct{}
	done        chan struct{}
}

func NewServer(input io.Reader, output, stderr io.Writer) *Server {
	return &Server{
		inputSource: input,
		input:       bufio.NewReader(input),
		output:      bufio.NewWriter(output),
		stderr:      stderr,
		watches:     make(map[string]watch),
		paths:       make(map[string]struct{}),
		done:        make(chan struct{}),
	}
}

func (server *Server) Run() error {
	defer close(server.done)
	if closer, ok := server.inputSource.(io.Closer); ok {
		defer closer.Close()
	}
	if err := server.send(protocol.PluginReply{Version: protocol.Version, Type: "ready"}); err != nil {
		return fmt.Errorf("send readiness: %w", err)
	}
	frames := make(chan frameResult, 1)
	go server.readFrames(frames)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case result := <-frames:
			if errors.Is(result.err, io.EOF) {
				return nil
			}
			if result.err != nil {
				if errors.Is(result.err, protocol.ErrFrameTooLarge) || errors.Is(result.err, protocol.ErrUnterminatedFrame) {
					return result.err
				}
				server.report("invalid daemon frame: %v", result.err)
				continue
			}
			message, err := protocol.DecodeDaemonFrame(result.data)
			if err != nil {
				server.report("invalid daemon frame: %v", err)
				continue
			}
			if message.Type == "shutdown" {
				return nil
			}
			if err := server.handleWatch(message); err != nil {
				return fmt.Errorf("handle watch request: %w", err)
			}
		case <-ticker.C:
			if err := server.scanWatches(); err != nil {
				return err
			}
		}
	}
}

type frameResult struct {
	data []byte
	err  error
}

func (server *Server) readFrames(output chan<- frameResult) {
	for {
		data, err := protocol.ReadFrame(server.input, pulse.MaxFrameBytes)
		select {
		case output <- frameResult{data: data, err: err}:
		case <-server.done:
			return
		}
		if err != nil && !errors.Is(err, protocol.ErrInvalidFrame) {
			return
		}
	}
}

func (server *Server) handleWatch(frame protocol.PluginFrame) error {
	var args watchArgs
	if err := jsonstrict.Decode(frame.WatchArgs, &args); err != nil {
		return server.rejectWatch(frame.RequestID, "watch_args must contain only trigger_file")
	}
	path, key, err := validateTriggerFile(args.TriggerFile)
	if err != nil {
		return server.rejectWatch(frame.RequestID, err.Error())
	}
	if _, exists := server.paths[key]; exists {
		return server.rejectWatch(frame.RequestID, "another watch already uses this trigger_file")
	}
	accepted := true
	if err := server.send(protocol.PluginReply{Version: protocol.Version, Type: "watch_result", RequestID: frame.RequestID, Accepted: &accepted}); err != nil {
		return err
	}
	server.watches[frame.SubscriptionID] = watch{subscriptionID: frame.SubscriptionID, path: path, pathKey: key}
	server.paths[key] = struct{}{}
	return nil
}

func validateTriggerFile(value string) (string, string, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return "", "", errors.New("trigger_file must be an absolute path")
	}
	path := filepath.Clean(value)
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("trigger_file parent directory must exist")
	}
	if !isLocalDirectory(parent) {
		return "", "", errors.New("trigger_file must be in a local directory")
	}
	if _, err := os.Lstat(path); err == nil {
		return "", "", errors.New("trigger_file must not exist when registered")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect trigger_file: %w", err)
	}
	probe, err := os.CreateTemp(parent, ".agent-pulse-write-check-*")
	if err != nil {
		return "", "", errors.New("trigger_file parent directory must be writable")
	}
	probeName := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probeName)
	if closeErr != nil || removeErr != nil {
		return "", "", errors.New("trigger_file parent directory write check failed")
	}
	return path, normalizePath(path), nil
}

func normalizePath(path string) string {
	path = filepath.Clean(path)
	if os.PathSeparator == '\\' {
		return strings.ToUpper(path)
	}
	return path
}

func (server *Server) rejectWatch(requestID, reason string) error {
	accepted := false
	return server.send(protocol.PluginReply{Version: protocol.Version, Type: "watch_result", RequestID: requestID, Accepted: &accepted, Error: reason})
}

func (server *Server) scanWatches() error {
	for _, current := range server.watches {
		claimed, err := claim(current)
		if err != nil {
			server.report("claim trigger file %q: %v", current.path, err)
			continue
		}
		if claimed == "" {
			continue
		}
		context, err := readClaimed(claimed)
		removeErr := os.Remove(claimed)
		if err != nil {
			server.report("read claimed trigger file %q: %v", claimed, err)
			continue
		}
		if removeErr != nil {
			server.report("remove claimed trigger file %q: %v", claimed, removeErr)
			continue
		}
		if err := server.send(protocol.PluginReply{
			Version:        protocol.Version,
			Type:           "event",
			SubscriptionID: current.subscriptionID,
			Context:        context,
		}); err != nil {
			return fmt.Errorf("send plugin event: %w", err)
		}
	}
	return nil
}

func claim(current watch) (string, error) {
	if _, err := os.Lstat(current.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	identifier, err := pulse.NewID()
	if err != nil {
		return "", err
	}
	claimed := filepath.Join(filepath.Dir(current.path), ".agent-pulse-"+identifier+".claim")
	if err := os.Rename(current.path, claimed); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return claimed, nil
}

func readClaimed(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, pulse.MaxContextBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > pulse.MaxContextBytes {
		return "", fmt.Errorf("context exceeds %d UTF-8 bytes", pulse.MaxContextBytes)
	}
	if !utf8.Valid(data) || len(data) == 0 {
		return "", errors.New("context must be nonempty valid UTF-8")
	}
	return string(data), nil
}

func (server *Server) send(frame protocol.PluginReply) error {
	line, err := protocol.EncodeLine(frame)
	if err != nil {
		return err
	}
	if _, err := io.Copy(server.output, bytes.NewReader(line)); err != nil {
		return err
	}
	return server.output.Flush()
}

func (server *Server) report(format string, values ...any) {
	message := fmt.Sprintf(format, values...)
	message = strings.Map(func(character rune) rune {
		if character < 0x20 && character != '\t' {
			return ' '
		}
		if character == 0x7f {
			return ' '
		}
		return character
	}, message)
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, _ = fmt.Fprintln(server.stderr, message)
}

func EncodeTriggerContext(context string) ([]byte, error) {
	if !utf8.ValidString(context) || len([]byte(context)) == 0 || len([]byte(context)) > pulse.MaxContextBytes {
		return nil, errors.New("context must be nonempty valid UTF-8 within the configured size limit")
	}
	return json.Marshal(context)
}
