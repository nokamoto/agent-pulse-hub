package modeloutput

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:generate go run go.uber.org/mock/mockgen -source=plugin.go -destination=plugin_mock_test.go -package=modeloutput

type outputFile interface {
	Write([]byte) (int, error)
	Close() error
}

type outputFiles interface {
	Stat(string) (os.FileInfo, error)
	Open(string) (io.ReadCloser, error)
	OpenAppend(string) (outputFile, error)
}

type localFiles struct{}

func (localFiles) Stat(path string) (os.FileInfo, error)   { return os.Stat(path) }
func (localFiles) Open(path string) (io.ReadCloser, error) { return os.Open(path) }
func (localFiles) OpenAppend(path string) (outputFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

// Plugin implements the local JSONL output profile through the public Session.
type Plugin struct {
	notifications bool
	logger        *slog.Logger
	files         outputFiles

	mu         sync.Mutex
	deliveryMu sync.Mutex
	active     bool
}

func New(logger *slog.Logger) *Plugin {
	return newWithFiles(logger, localFiles{})
}

func newWithFiles(logger *slog.Logger, files outputFiles) *Plugin {
	if logger == nil {
		logger = slog.Default()
	}
	return &Plugin{logger: logger, files: files}
}

func (p *Plugin) WithNotifications(enabled bool) *Plugin { p.notifications = enabled; return p }

func (p *Plugin) Session(stream pluginprotocol.SessionServer) error {
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		return status.Error(codes.AlreadyExists, "an output Session is already active")
	}
	p.active = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active = false
		p.mu.Unlock()
	}()

	ready := pluginprotocol.Frame{Type: "ready", Role: "output"}
	if p.notifications {
		ready.Capabilities = []string{pluginprotocol.NotificationCapability}
	}
	if err := p.send(stream, ready); err != nil {
		return fmt.Errorf("send readiness: %w", err)
	}
	for {
		payload, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || stream.Context().Err() != nil {
				return nil
			}
			return fmt.Errorf("receive daemon message: %w", err)
		}
		frame, err := pluginprotocol.ParsePayload(payload, pluginprotocol.DaemonToPlugin)
		if err != nil {
			p.logger.Error("model_output_request_rejected", "reason", safeText(err.Error(), 256))
			return err
		}
		switch frame.Type {
		case "validate_destination":
			if err := p.validate(frame.DestinationArgs); err != nil {
				if sendErr := p.send(stream, pluginprotocol.Frame{
					Type: "destination_result", RequestID: frame.RequestID,
					Accepted: boolPointer(false), Error: "output_file destination is invalid",
				}); sendErr != nil {
					return sendErr
				}
				p.logger.Warn("model_output_destination_rejected", "request_id", frame.RequestID, "reason", safeText(err.Error(), 256))
				continue
			}
			if err := p.send(stream, pluginprotocol.Frame{
				Type: "destination_result", RequestID: frame.RequestID, Accepted: boolPointer(true),
			}); err != nil {
				return err
			}
		case "notification_deliver":
			if !p.notifications {
				return errors.New("notification profile was not selected")
			}
			result := p.deliverNotification(frame)
			if err := p.send(stream, result); err != nil {
				return err
			}
		case "deliver":
			if p.notifications {
				return errors.New("legacy delivery is unavailable in notification mode")
			}

			outcome, detail := p.deliver(frame)
			result := pluginprotocol.Frame{Type: "delivery_result", RequestID: frame.RequestID, Status: outcome}
			if outcome != "accepted" {
				result.Error = detail
			}
			if err := p.send(stream, result); err != nil {
				return err
			}
			p.logger.Info("model_output_delivery", "delivery_id", frame.DeliveryID,
				"subscription_id", frame.SubscriptionID, "output_file", destinationPath(frame.DestinationArgs),
				"outcome", outcome, "detail", safeText(detail, 256))
		default:
			return fmt.Errorf("unexpected daemon message %q", frame.Type)
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

func (p *Plugin) validate(arguments *jsonvalue.Value) error {
	path, err := parseDestination(arguments)
	if err != nil {
		return err
	}
	parent := filepath.Dir(path)
	parentInfo, err := p.files.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return errors.New("output_file parent must be an existing directory")
	}
	if parentInfo.Mode().Perm()&0o222 == 0 {
		return errors.New("output_file parent is not writable")
	}
	info, err := p.files.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("output_file path is unavailable")
	}
	if !info.Mode().IsRegular() {
		return errors.New("output_file must be absent or a regular file")
	}
	return p.validateJSONL(path)
}

func parseDestination(arguments *jsonvalue.Value) (string, error) {
	if err := protocol.ValidateFields(arguments, []string{"output_file"}, "output_file"); err != nil {
		return "", errors.New("destination arguments must contain only output_file")
	}
	path, ok := protocol.StringField(arguments, "output_file")
	if !ok || !filepath.IsAbs(path) {
		return "", errors.New("output_file must be an absolute path")
	}
	if filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return "", errors.New("output_file must name a file")
	}
	return path, nil
}

func (p *Plugin) validateJSONL(path string) error {
	file, err := p.files.Open(path)
	if err != nil {
		return errors.New("output_file could not be read")
	}
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = file.Close()
			return errors.New("output_file could not be read completely")
		}
		if len(line) != 0 {
			lineNumber++
			if readErr == io.EOF {
				_ = file.Close()
				return fmt.Errorf("output_file line %d is not LF terminated", lineNumber)
			}
			line = line[:len(line)-1]
			if !utf8.Valid(line) {
				_ = file.Close()
				return fmt.Errorf("output_file line %d is not UTF-8", lineNumber)
			}
			value, parseErr := jsonvalue.Parse(line, 0)
			if parseErr != nil || value.Kind() != jsonvalue.Object {
				_ = file.Close()
				return fmt.Errorf("output_file line %d is not a strict JSON object", lineNumber)
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if err := file.Close(); err != nil {
				return errors.New("output_file could not be closed after validation")
			}
			return nil
		}
		return errors.New("output_file could not be read completely")
	}
}

func (p *Plugin) deliver(frame pluginprotocol.Frame) (string, string) {
	p.deliveryMu.Lock()
	defer p.deliveryMu.Unlock()
	path := destinationPath(frame.DestinationArgs)
	if err := p.validate(frame.DestinationArgs); err != nil {
		return "failed", "output_file destination is invalid"
	}
	line, err := encodeRecord(frame, time.Now().UTC())
	if err != nil {
		return "failed", "delivery record could not be encoded"
	}
	file, err := p.files.OpenAppend(path)
	if err != nil {
		return "failed", "output_file could not be opened"
	}
	line = append(line, '\n')
	wrote, writeErr := writeRecord(file, line)
	closeErr := file.Close()
	if writeErr != nil {
		if wrote {
			return "unknown", "output_file write failed after writing began"
		}
		return "failed", "output_file write failed before writing data"
	}
	if closeErr != nil {
		return "unknown", "output_file close failed after writing"
	}
	return "accepted", ""
}

func encodeRecord(frame pluginprotocol.Frame, observed time.Time) ([]byte, error) {
	fields := map[string]*jsonvalue.Value{
		"delivery_id":       jsonvalue.NewString(frame.DeliveryID),
		"input_plugin":      jsonvalue.NewString(frame.InputPlugin),
		"subscription_id":   jsonvalue.NewString(frame.SubscriptionID),
		"context":           jsonvalue.NewString(frame.Context),
		"external_data":     jsonvalue.NewBoolean(true),
		"permission_notice": jsonvalue.NewString(pluginprotocol.PermissionNotice),
		"observed_at":       jsonvalue.NewString(observed.UTC().Format(time.RFC3339)),
	}
	return jsonvalue.NewObject(fields).Marshal(), nil
}

func writeRecord(file outputFile, data []byte) (bool, error) {
	wrote := false
	for len(data) > 0 {
		written, err := file.Write(data)
		if written > 0 {
			wrote = true
			data = data[written:]
		}
		if err != nil {
			return wrote, err
		}
		if written == 0 {
			return wrote, io.ErrShortWrite
		}
	}
	return wrote, nil
}

func destinationPath(arguments *jsonvalue.Value) string {
	if arguments == nil {
		return ""
	}
	path, _ := protocol.StringField(arguments, "output_file")
	return path
}

func boolPointer(value bool) *bool { return &value }

func safeText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
}
