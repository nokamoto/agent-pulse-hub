package pluginprotocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxContextBytes = 8_192

type Direction uint8

const (
	PluginToDaemon Direction = iota + 1
	DaemonToPlugin
)

type Frame struct {
	Type           string
	RequestID      string
	SubscriptionID string
	WatchArgs      *jsonvalue.Value
	Accepted       *bool
	Error          string
	Context        string
}

func Decode(value *jsonvalue.Value, direction Direction) (Frame, error) {
	if value == nil || value.Kind() != jsonvalue.Object {
		return Frame{}, errors.New("frame must be a JSON object")
	}
	version, ok := protocol.NumberField(value, "version")
	if !ok || version != "1" {
		return Frame{}, errors.New("unsupported or invalid protocol version")
	}
	typeName, ok := protocol.StringField(value, "type")
	if !ok || typeName == "" {
		return Frame{}, errors.New("frame type must be a nonempty string")
	}
	frame := Frame{Type: typeName}
	switch direction {
	case PluginToDaemon:
		switch typeName {
		case "ready":
			if err := protocol.ValidateFields(value, []string{"version", "type"}, "version", "type"); err != nil {
				return Frame{}, err
			}
		case "watch_result":
			if err := protocol.ValidateFields(value, []string{"version", "type", "request_id", "accepted"}, "version", "type", "request_id", "accepted", "error"); err != nil {
				return Frame{}, err
			}
			requestID, ok := protocol.StringField(value, "request_id")
			if !ok || requestID == "" {
				return Frame{}, errors.New("watch_result request_id must be nonempty")
			}
			accepted, ok := protocol.BooleanField(value, "accepted")
			if !ok {
				return Frame{}, errors.New("watch_result accepted must be boolean")
			}
			frame.RequestID = requestID
			frame.Accepted = &accepted
			if accepted {
				if _, exists := value.Get("error"); exists {
					return Frame{}, errors.New("accepted watch_result must not contain error")
				}
			} else {
				message, ok := protocol.StringField(value, "error")
				if !ok || message == "" {
					return Frame{}, errors.New("rejected watch_result requires a nonempty error")
				}
				frame.Error = message
			}
		case "event":
			if err := protocol.ValidateFields(value, []string{"version", "type", "subscription_id", "context"}, "version", "type", "subscription_id", "context"); err != nil {
				return Frame{}, err
			}
			subscriptionID, ok := protocol.StringField(value, "subscription_id")
			if !ok || subscriptionID == "" {
				return Frame{}, errors.New("event subscription_id must be nonempty")
			}
			contextText, ok := protocol.StringField(value, "context")
			if !ok || contextText == "" || !utf8.ValidString(contextText) || len([]byte(contextText)) > MaxContextBytes {
				return Frame{}, fmt.Errorf("event context must contain 1 to %d UTF-8 bytes", MaxContextBytes)
			}
			frame.SubscriptionID = subscriptionID
			frame.Context = contextText
		default:
			return Frame{}, fmt.Errorf("invalid plugin-to-daemon frame type %q", typeName)
		}
	case DaemonToPlugin:
		switch typeName {
		case "watch":
			if err := protocol.ValidateFields(value, []string{"version", "type", "request_id", "subscription_id", "watch_args"}, "version", "type", "request_id", "subscription_id", "watch_args"); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			watchArgs, argsOK := protocol.ObjectField(value, "watch_args")
			if !requestOK || requestID == "" || !subscriptionOK || subscriptionID == "" || !argsOK {
				return Frame{}, errors.New("watch fields have invalid types or empty identifiers")
			}
			frame.RequestID = requestID
			frame.SubscriptionID = subscriptionID
			frame.WatchArgs = watchArgs
		case "shutdown":
			if err := protocol.ValidateFields(value, []string{"version", "type"}, "version", "type"); err != nil {
				return Frame{}, err
			}
		default:
			return Frame{}, fmt.Errorf("invalid daemon-to-plugin frame type %q", typeName)
		}
	default:
		return Frame{}, errors.New("invalid frame direction")
	}
	return frame, nil
}

func Encode(frame Frame) ([]byte, error) {
	fields := map[string]*jsonvalue.Value{
		"version": jsonvalue.NewNumber("1"),
		"type":    jsonvalue.NewString(frame.Type),
	}
	switch frame.Type {
	case "ready", "shutdown":
	case "watch":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["watch_args"] = frame.WatchArgs
	case "watch_result":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		if frame.Accepted != nil {
			fields["accepted"] = jsonvalue.NewBoolean(*frame.Accepted)
		}
		if frame.Error != "" {
			fields["error"] = jsonvalue.NewString(frame.Error)
		}
	case "event":
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["context"] = jsonvalue.NewString(frame.Context)
	default:
		return nil, fmt.Errorf("unsupported frame type %q", frame.Type)
	}
	value := jsonvalue.NewObject(fields)
	if _, err := Decode(value, directionFor(frame.Type)); err != nil {
		return nil, err
	}
	return protocol.EncodeFrame(value)
}

func directionFor(typeName string) Direction {
	switch typeName {
	case "ready", "watch_result", "event":
		return PluginToDaemon
	default:
		return DaemonToPlugin
	}
}

func Read(reader *bufio.Reader) (Frame, error) {
	line, err := protocol.ReadFrame(reader, protocol.MaxFrameBytes)
	if err != nil {
		return Frame{}, err
	}
	value, err := protocol.ParseFrame(line)
	if err != nil {
		return Frame{}, err
	}
	return Decode(value, PluginToDaemon)
}

func Write(writer io.Writer, frame Frame) error {
	bytes, err := Encode(frame)
	if err != nil {
		return err
	}
	for len(bytes) > 0 {
		written, err := writer.Write(bytes)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		bytes = bytes[written:]
	}
	return nil
}
