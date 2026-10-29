package pluginprotocol

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func StreamCounter(value string, positive bool) (uint64, error) {
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil || strconv.FormatUint(n, 10) != value || (positive && n == 0) {
		return 0, errors.New("invalid stream counter")
	}
	return n, nil
}

func decodeStreamFrame(value *jsonvalue.Value, frame Frame) (Frame, error) {
	fields := []string{"version", "type", "request_id", "subscription_id"}
	switch frame.Type {
	case "state_stream_watch":
		fields = append(fields, "watch_args", "schema", "chunk_count")
	case "checkpoint_stream_begin":
		fields = append(fields, "schema")
	case "state_stream_chunk", "checkpoint_stream_chunk":
		fields = append(fields, "index", "data")
	default:
		fields = append(fields, "chunk_count")
	}
	if err := protocol.ValidateFields(value, fields, fields...); err != nil {
		return Frame{}, err
	}
	var ok bool
	frame.RequestID, ok = protocol.StringField(value, "request_id")
	if !ok || frame.RequestID == "" {
		return Frame{}, errors.New("stream request_id must be nonempty")
	}
	frame.SubscriptionID, ok = protocol.StringField(value, "subscription_id")
	if !ok || frame.SubscriptionID == "" {
		return Frame{}, errors.New("stream subscription_id must be nonempty")
	}
	if frame.Type == "state_stream_watch" || frame.Type == "checkpoint_stream_begin" {
		schema, _ := value.Get("schema")
		if schema.Kind() != jsonvalue.Null {
			frame.Schema, ok = jsonvalue.ParseString(schema)
			if !ok || frame.Schema == "" || !utf8.ValidString(frame.Schema) || len(frame.Schema) > MaxRuntimeSchemaBytes {
				return Frame{}, errors.New("invalid stream schema")
			}
		} else if frame.Type != "state_stream_watch" {
			return Frame{}, errors.New("stream schema must be nonempty")
		}
	}
	if frame.Type == "state_stream_watch" {
		frame.WatchArgs, ok = protocol.ObjectField(value, "watch_args")
		if !ok {
			return Frame{}, errors.New("invalid stream watch arguments")
		}
	}
	if frame.Type == "state_stream_chunk" || frame.Type == "checkpoint_stream_chunk" {
		frame.Index, ok = protocol.StringField(value, "index")
		if !ok {
			return Frame{}, errors.New("invalid stream index")
		}
		if _, err := StreamCounter(frame.Index, false); err != nil {
			return Frame{}, err
		}
		frame.Data, ok = protocol.ObjectField(value, "data")
		if !ok || len(frame.Data.Marshal()) > MaxRuntimeStateBytes {
			return Frame{}, errors.New("invalid or oversized stream data")
		}
	}
	if frame.Type == "state_stream_watch" || frame.Type == "state_stream_end" || frame.Type == "checkpoint_stream_end" {
		frame.ChunkCount, ok = protocol.StringField(value, "chunk_count")
		if !ok {
			return Frame{}, errors.New("invalid stream count")
		}
		n, err := StreamCounter(frame.ChunkCount, frame.Type == "checkpoint_stream_end")
		if err != nil {
			return Frame{}, err
		}
		if frame.Type == "state_stream_watch" && ((frame.Schema == "") != (n == 0)) {
			return Frame{}, errors.New("stream schema and count disagree")
		}
	}
	return frame, nil
}

func encodeStreamFields(fields map[string]*jsonvalue.Value, frame Frame) {
	fields["request_id"] = jsonvalue.NewString(frame.RequestID)
	fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
	switch frame.Type {
	case "state_stream_watch":
		fields["watch_args"] = frame.WatchArgs
		fields["schema"] = jsonvalue.NewNull()
		if frame.Schema != "" {
			fields["schema"] = jsonvalue.NewString(frame.Schema)
		}
		fields["chunk_count"] = jsonvalue.NewString(frame.ChunkCount)
	case "checkpoint_stream_begin":
		fields["schema"] = jsonvalue.NewString(frame.Schema)
	case "state_stream_chunk", "checkpoint_stream_chunk":
		fields["index"] = jsonvalue.NewString(frame.Index)
		fields["data"] = frame.Data
	default:
		fields["chunk_count"] = jsonvalue.NewString(frame.ChunkCount)
	}
}
