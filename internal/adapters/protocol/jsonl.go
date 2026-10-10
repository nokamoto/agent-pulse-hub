package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxFrameBytes = 65_536

var (
	ErrFrameTooLarge = errors.New("frame exceeds the maximum size")
	ErrPartialFrame  = errors.New("stream ended before the frame delimiter")
)

// ReadFrame reads one LF or CRLF terminated frame. The byte limit includes the
// delimiter. The returned bytes exclude the delimiter and its optional CR.
func ReadFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("frame limit must be positive")
	}
	frame := make([]byte, 0, min(limit, 4096))
	for len(frame) < limit {
		value, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && len(frame) != 0 {
				return nil, ErrPartialFrame
			}
			return nil, err
		}
		if value == '\n' {
			if len(frame) > 0 && frame[len(frame)-1] == '\r' {
				frame = frame[:len(frame)-1]
			}
			return frame, nil
		}
		frame = append(frame, value)
	}
	return nil, ErrFrameTooLarge
}

// EncodeFrame emits a compact JSON value with one LF delimiter.
func EncodeFrame(value *jsonvalue.Value) ([]byte, error) {
	frame := value.Marshal()
	if len(frame)+1 > MaxFrameBytes {
		return nil, ErrFrameTooLarge
	}
	return append(frame, '\n'), nil
}

func ParseFrame(frame []byte) (*jsonvalue.Value, error) {
	return jsonvalue.Parse(frame, MaxFrameBytes-1)
}

func ValidateFields(value *jsonvalue.Value, required []string, allowed ...string) error {
	if value == nil || value.Kind() != jsonvalue.Object {
		return errors.New("expected a JSON object")
	}
	fields := value.Fields()
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name := range fields {
		if _, ok := allowedSet[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing field %q", name)
		}
	}
	return nil
}

func StringField(value *jsonvalue.Value, name string) (string, bool) {
	field, ok := value.Get(name)
	if !ok {
		return "", false
	}
	return jsonvalue.ParseString(field)
}

func NumberField(value *jsonvalue.Value, name string) (string, bool) {
	field, ok := value.Get(name)
	if !ok || field.Kind() != jsonvalue.Number {
		return "", false
	}
	return field.Text()
}

func ObjectField(value *jsonvalue.Value, name string) (*jsonvalue.Value, bool) {
	field, ok := value.Get(name)
	return field, ok && field.Kind() == jsonvalue.Object
}

func BooleanField(value *jsonvalue.Value, name string) (bool, bool) {
	field, ok := value.Get(name)
	if !ok {
		return false, false
	}
	return field.Bool()
}
