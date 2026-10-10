package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/jsonstrict"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/pulse"
)

const Version = 1

var (
	ErrFrameTooLarge      = errors.New("frame exceeds maximum size")
	ErrUnterminatedFrame  = errors.New("frame is not newline terminated")
	ErrInvalidFrame       = errors.New("invalid protocol frame")
	ErrInvalidControlData = errors.New("invalid control request")
)

type ControlRequest struct {
	Version   int             `json:"version"`
	Operation string          `json:"op"`
	Plugin    string          `json:"plugin"`
	SessionID string          `json:"session_id"`
	WatchArgs json.RawMessage `json:"watch_args"`
}

type ControlResponse struct {
	Version        int    `json:"version"`
	OK             bool   `json:"ok"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
}

type PluginFrame struct {
	Version        int
	Type           string
	RequestID      string
	SubscriptionID string
	WatchArgs      json.RawMessage
	Accepted       bool
	Error          string
	Context        string
}

type WatchFrame struct {
	Version        int             `json:"version"`
	Type           string          `json:"type"`
	RequestID      string          `json:"request_id"`
	SubscriptionID string          `json:"subscription_id"`
	WatchArgs      json.RawMessage `json:"watch_args"`
}

type ShutdownFrame struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
}

type PluginReply struct {
	Version        int    `json:"version"`
	Type           string `json:"type"`
	RequestID      string `json:"request_id,omitempty"`
	Accepted       *bool  `json:"accepted,omitempty"`
	Error          string `json:"error,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	Context        string `json:"context,omitempty"`
}

func ReadFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	var frame []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(frame)+len(part) > limit {
			return nil, ErrFrameTooLarge
		}
		frame = append(frame, part...)
		if err == nil {
			frame = bytes.TrimSuffix(frame, []byte{'\n'})
			frame = bytes.TrimSuffix(frame, []byte{'\r'})
			if !utf8.Valid(frame) {
				return nil, fmt.Errorf("%w: frame is not valid UTF-8", ErrInvalidFrame)
			}
			return frame, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(frame) != 0 {
				return nil, ErrUnterminatedFrame
			}
			return nil, io.EOF
		}
		return nil, err
	}
}

func DecodeControlRequest(data []byte) (ControlRequest, error) {
	var request ControlRequest
	if err := jsonstrict.Decode(data, &request); err != nil {
		return ControlRequest{}, fmt.Errorf("%w: %v", ErrInvalidControlData, err)
	}
	if request.Version != Version || request.Operation != "register" || request.Plugin == "" {
		return ControlRequest{}, ErrInvalidControlData
	}
	if err := pulse.ValidateUUID(request.SessionID); err != nil {
		return ControlRequest{}, fmt.Errorf("%w: session_id must be a UUID", ErrInvalidControlData)
	}
	if _, err := pulse.CanonicalWatchArgs(request.WatchArgs); err != nil {
		return ControlRequest{}, fmt.Errorf("%w: watch_args must be a JSON object", ErrInvalidControlData)
	}
	return request, nil
}

func DecodeControlResponse(data []byte) (ControlResponse, error) {
	if err := jsonstrict.Check(data); err != nil {
		return ControlResponse{}, fmt.Errorf("decode response: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ControlResponse{}, errors.New("decode response: expected an object")
	}
	var response ControlResponse
	if err := jsonstrict.Decode(data, &response); err != nil {
		return ControlResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if response.Version != Version {
		return ControlResponse{}, errors.New("unsupported control response version")
	}
	if response.OK {
		if !exactFields(fields, "version", "ok", "subscription_id") || pulse.ValidateUUID(response.SubscriptionID) != nil {
			return ControlResponse{}, errors.New("malformed successful response")
		}
	} else if !exactFields(fields, "version", "ok", "code", "message") || response.Code == "" || response.Message == "" {
		return ControlResponse{}, errors.New("malformed error response")
	}
	return response, nil
}

func DecodePluginFrame(data []byte) (PluginFrame, error) {
	if err := jsonstrict.Check(data); err != nil {
		return PluginFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return PluginFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	if fields == nil {
		return PluginFrame{}, ErrInvalidFrame
	}
	var header struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Version != Version || header.Type == "" {
		return PluginFrame{}, ErrInvalidFrame
	}
	frame := PluginFrame{Version: header.Version, Type: header.Type}
	switch header.Type {
	case "ready":
		if !exactFields(fields, "version", "type") {
			return PluginFrame{}, ErrInvalidFrame
		}
	case "watch_result":
		if !exactFields(fields, "version", "type", "request_id", "accepted") && !exactFields(fields, "version", "type", "request_id", "accepted", "error") {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := unmarshalString(fields, "request_id", &frame.RequestID); err != nil || frame.RequestID == "" {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := json.Unmarshal(fields["accepted"], &frame.Accepted); err != nil {
			return PluginFrame{}, ErrInvalidFrame
		}
		_, hasError := fields["error"]
		if raw, ok := fields["error"]; ok {
			if err := json.Unmarshal(raw, &frame.Error); err != nil {
				return PluginFrame{}, ErrInvalidFrame
			}
		}
		if !frame.Accepted && (!hasError || frame.Error == "") {
			return PluginFrame{}, ErrInvalidFrame
		}
		if frame.Accepted && hasError {
			return PluginFrame{}, ErrInvalidFrame
		}
	case "event":
		if !exactFields(fields, "version", "type", "subscription_id", "context") {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := unmarshalString(fields, "subscription_id", &frame.SubscriptionID); err != nil {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := unmarshalString(fields, "context", &frame.Context); err != nil {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := pulse.ValidateEvent(pulse.Event{SubscriptionID: frame.SubscriptionID, Context: frame.Context}); err != nil {
			return PluginFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
		}
	default:
		return PluginFrame{}, fmt.Errorf("%w: unsupported plugin frame type %q", ErrInvalidFrame, header.Type)
	}
	return frame, nil
}

func DecodeDaemonFrame(data []byte) (PluginFrame, error) {
	if err := jsonstrict.Check(data); err != nil {
		return PluginFrame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return PluginFrame{}, ErrInvalidFrame
	}
	var header struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Version != Version {
		return PluginFrame{}, ErrInvalidFrame
	}
	frame := PluginFrame{Version: header.Version, Type: header.Type}
	switch header.Type {
	case "watch":
		if !exactFields(fields, "version", "type", "request_id", "subscription_id", "watch_args") {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := unmarshalString(fields, "request_id", &frame.RequestID); err != nil || frame.RequestID == "" {
			return PluginFrame{}, ErrInvalidFrame
		}
		if err := unmarshalString(fields, "subscription_id", &frame.SubscriptionID); err != nil || frame.SubscriptionID == "" {
			return PluginFrame{}, ErrInvalidFrame
		}
		frame.WatchArgs = append(json.RawMessage(nil), fields["watch_args"]...)
		if _, err := pulse.CanonicalWatchArgs(frame.WatchArgs); err != nil {
			return PluginFrame{}, ErrInvalidFrame
		}
	case "shutdown":
		if !exactFields(fields, "version", "type") {
			return PluginFrame{}, ErrInvalidFrame
		}
	default:
		return PluginFrame{}, fmt.Errorf("%w: unsupported daemon frame type %q", ErrInvalidFrame, header.Type)
	}
	return frame, nil
}

func EncodeLine(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded)+1 > pulse.MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	return append(encoded, '\n'), nil
}

func exactFields(fields map[string]json.RawMessage, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

func unmarshalString(fields map[string]json.RawMessage, name string, destination *string) error {
	raw, ok := fields[name]
	if !ok {
		return errors.New("missing string field")
	}
	return json.Unmarshal(raw, destination)
}
