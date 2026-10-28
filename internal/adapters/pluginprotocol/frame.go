package pluginprotocol

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	MaxContextBytes              = 8_192
	MaxRuntimeStateBytes         = 32_768
	MaxRuntimeSchemaBytes        = 128
	RuntimeStateCapability       = "runtime_state_v1"
	RuntimeStateV2Capability     = "runtime_state_v2"
	RuntimeStateStreamCapability = "runtime_state_stream_v1"
	CancelWatchCapability        = "cancel_watch_v1"
	RewaitCapability             = "rewait_v1"
	PermissionNotice             = "External data; no additional permissions are granted."
)

type Direction uint8

const (
	PluginToDaemon Direction = iota + 1
	DaemonToPlugin
)

type Frame struct {
	Type                 string
	Role                 string
	Capabilities         []string
	RequestID            string
	SubscriptionID       string
	Generation           string
	ConfirmationPosition string
	EventID              string
	DeliveryID           string
	InputPlugin          string
	WatchArgs            *jsonvalue.Value
	State                *jsonvalue.Value
	Schema               string
	ChunkCount           string
	Index                string
	Data                 *jsonvalue.Value
	DestinationArgs      *jsonvalue.Value
	Accepted             *bool
	Status               string
	Quiesce              bool
	Error                string
	Context              string
	ExternalData         *bool
	PermissionNotice     string
}

func Decode(value *jsonvalue.Value, direction Direction) (Frame, error) {
	if value == nil || value.Kind() != jsonvalue.Object {
		return Frame{}, errors.New("frame must be a JSON object")
	}
	version, ok := protocol.NumberField(value, "version")
	if !ok || version != "2" {
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
			if err := protocol.ValidateFields(value, []string{"version", "type", "role"}, "version", "type", "role", "capabilities"); err != nil {
				return Frame{}, err
			}
			role, ok := protocol.StringField(value, "role")
			if !ok || (role != "input" && role != "output") {
				return Frame{}, errors.New("ready role must be input or output")
			}
			frame.Role = role
			if capabilities, exists := value.Get("capabilities"); exists {
				if role != "input" || capabilities.Kind() != jsonvalue.Array {
					return Frame{}, errors.New("capabilities are only valid as an input array")
				}
				items := capabilities.Items()
				frame.Capabilities = make([]string, 0, len(items))
				seen := make(map[string]struct{}, len(items))
				for _, item := range items {
					capability, ok := jsonvalue.ParseString(item)
					if !ok || (capability != RuntimeStateCapability && capability != RuntimeStateV2Capability && capability != RewaitCapability && capability != RuntimeStateStreamCapability && capability != CancelWatchCapability) {
						return Frame{}, errors.New("ready contains an unknown or invalid capability")
					}
					if _, duplicate := seen[capability]; duplicate {
						return Frame{}, errors.New("ready contains a duplicate capability")
					}
					seen[capability] = struct{}{}
					frame.Capabilities = append(frame.Capabilities, capability)
				}
				_, v1 := seen[RuntimeStateCapability]
				_, v2 := seen[RuntimeStateV2Capability]
				_, rewait := seen[RewaitCapability]
				_, stream := seen[RuntimeStateStreamCapability]
				if (v1 && v2) || ((rewait || stream) && (!v2 || v1)) {
					return Frame{}, errors.New("ready must select exactly one runtime-state revision")
				}
			}
		case "checkpoint_stream_begin", "checkpoint_stream_chunk", "checkpoint_stream_end":
			return decodeStreamFrame(value, frame)
		case "watch_result", "destination_result", "resume_result", "cancel_result":
			if err := protocol.ValidateFields(value, []string{"version", "type", "request_id", "accepted"}, "version", "type", "request_id", "accepted", "error"); err != nil {
				return Frame{}, err
			}
			requestID, ok := protocol.StringField(value, "request_id")
			if !ok || requestID == "" {
				return Frame{}, errors.New("result request_id must be nonempty")
			}
			accepted, ok := protocol.BooleanField(value, "accepted")
			if !ok {
				return Frame{}, errors.New("result accepted must be boolean")
			}
			frame.RequestID = requestID
			frame.Accepted = &accepted
			if accepted {
				if _, exists := value.Get("error"); exists {
					return Frame{}, errors.New("accepted result must not contain error")
				}
			} else {
				message, ok := protocol.StringField(value, "error")
				if !ok || message == "" {
					return Frame{}, errors.New("rejected result requires a nonempty error")
				}
				frame.Error = message
			}
			if (typeName == "resume_result" || typeName == "cancel_result") && len(frame.Error) > 1024 {
				return Frame{}, errors.New("result error exceeds the message limit")
			}
		case "event":
			if err := protocol.ValidateFields(value, []string{"version", "type", "subscription_id", "context"}, "version", "type", "subscription_id", "context"); err != nil {
				return Frame{}, err
			}
			subscriptionID, idOK := protocol.StringField(value, "subscription_id")
			contextText, contextOK := protocol.StringField(value, "context")
			if !idOK || subscriptionID == "" || !contextOK || contextText == "" || !utf8.ValidString(contextText) || len([]byte(contextText)) > MaxContextBytes {
				return Frame{}, fmt.Errorf("event requires a nonempty subscription and context of at most %d UTF-8 bytes", MaxContextBytes)
			}
			frame.SubscriptionID = subscriptionID
			frame.Context = contextText
		case "state_event":
			if err := protocol.ValidateFields(value, []string{"version", "type", "subscription_id", "event_id", "context"}, "version", "type", "subscription_id", "event_id", "context", "generation"); err != nil {
				return Frame{}, err
			}
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			eventID, eventOK := protocol.StringField(value, "event_id")
			contextText, contextOK := protocol.StringField(value, "context")
			if !subscriptionOK || subscriptionID == "" || !eventOK || !validEventID(eventID) || !contextOK || !validContext(contextText) {
				return Frame{}, errors.New("state_event fields are invalid")
			}
			frame.SubscriptionID, frame.EventID, frame.Context = subscriptionID, eventID, contextText
			if generation, present := value.Get("generation"); present {
				text, valid := jsonvalue.ParseString(generation)
				if !valid || text == "" {
					return Frame{}, errors.New("generation must be nonempty")
				}
				frame.Generation = text
			}
		case "delivery_result":
			if err := protocol.ValidateFields(value, []string{"version", "type", "request_id", "status"}, "version", "type", "request_id", "status", "error"); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			status, statusOK := protocol.StringField(value, "status")
			if !requestOK || requestID == "" || !statusOK || (status != "accepted" && status != "failed" && status != "unknown") {
				return Frame{}, errors.New("delivery_result request_id or status is invalid")
			}
			message, exists := value.Get("error")
			if status == "accepted" && exists {
				return Frame{}, errors.New("accepted delivery_result must not contain error")
			}
			if status != "accepted" {
				text, ok := jsonvalue.ParseString(message)
				if !exists || !ok || text == "" {
					return Frame{}, errors.New("failed or unknown delivery_result requires a nonempty error")
				}
				frame.Error = text
			}
			frame.RequestID = requestID
			frame.Status = status
		case "checkpoint_result":
			if err := protocol.ValidateFields(value, []string{"version", "type", "request_id", "accepted"}, "version", "type", "request_id", "accepted", "state", "error"); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			accepted, acceptedOK := protocol.BooleanField(value, "accepted")
			if !requestOK || requestID == "" || !acceptedOK {
				return Frame{}, errors.New("checkpoint_result request_id or accepted is invalid")
			}
			state, hasState := value.Get("state")
			errorText, hasError := protocol.StringField(value, "error")
			_, errorPresent := value.Get("error")
			if accepted {
				if !hasState || errorPresent || ValidateStateEnvelope(state) != nil {
					return Frame{}, errors.New("accepted checkpoint_result requires a valid state and no error")
				}
				frame.State = state
			} else {
				if hasState || !hasError || errorText == "" {
					return Frame{}, errors.New("rejected checkpoint_result requires a nonempty error and no state")
				}
				frame.Error = errorText
			}
			frame.RequestID, frame.Accepted = requestID, &accepted
		default:
			return Frame{}, fmt.Errorf("invalid plugin-to-daemon frame type %q", typeName)
		}
	case DaemonToPlugin:
		switch typeName {
		case "state_stream_watch", "state_stream_chunk", "state_stream_end":
			return decodeStreamFrame(value, frame)
		case "cancel_watch":
			fields := []string{"version", "type", "request_id", "subscription_id"}
			if err := protocol.ValidateFields(value, fields, fields...); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			if !requestOK || requestID == "" || !subscriptionOK || subscriptionID == "" {
				return Frame{}, errors.New("cancel_watch identifiers must be nonempty strings")
			}
			frame.RequestID, frame.SubscriptionID = requestID, subscriptionID
		case "activate_watch":
			fields := []string{"version", "type", "subscription_id"}
			if err := protocol.ValidateFields(value, fields, append(fields, "generation")...); err != nil {
				return Frame{}, err
			}
			subscriptionID, ok := protocol.StringField(value, "subscription_id")
			if !ok || subscriptionID == "" {
				return Frame{}, errors.New("activate_watch subscription_id must be nonempty")
			}
			frame.SubscriptionID = subscriptionID
			if generation, present := value.Get("generation"); present {
				text, valid := jsonvalue.ParseString(generation)
				if !valid || text == "" {
					return Frame{}, errors.New("generation must be nonempty")
				}
				frame.Generation = text
			}
		case "resume_watch":
			fields := []string{"version", "type", "request_id", "subscription_id", "generation", "confirmation_position"}
			if err := protocol.ValidateFields(value, fields, fields...); err != nil {
				return Frame{}, err
			}
			for name, target := range map[string]*string{"request_id": &frame.RequestID, "subscription_id": &frame.SubscriptionID, "generation": &frame.Generation, "confirmation_position": &frame.ConfirmationPosition} {
				text, valid := protocol.StringField(value, name)
				if !valid || text == "" {
					return Frame{}, errors.New("resume_watch fields must be nonempty strings")
				}
				*target = text
			}
			if len(frame.ConfirmationPosition) > 4096 {
				return Frame{}, errors.New("confirmation position exceeds the byte limit")
			}
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
		case "state_watch":
			fields := []string{"version", "type", "request_id", "subscription_id", "watch_args", "state"}
			if err := protocol.ValidateFields(value, fields, fields...); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			watchArgs, argsOK := protocol.ObjectField(value, "watch_args")
			state, stateOK := value.Get("state")
			if !requestOK || requestID == "" || !subscriptionOK || subscriptionID == "" || !argsOK || !stateOK || (state.Kind() != jsonvalue.Null && ValidateStateEnvelope(state) != nil) {
				return Frame{}, errors.New("state_watch fields are invalid")
			}
			frame.RequestID, frame.SubscriptionID, frame.WatchArgs, frame.State = requestID, subscriptionID, watchArgs, state
		case "validate_destination":
			if err := protocol.ValidateFields(value, []string{"version", "type", "request_id", "destination_args"}, "version", "type", "request_id", "destination_args"); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			args, argsOK := protocol.ObjectField(value, "destination_args")
			if !requestOK || requestID == "" || !argsOK {
				return Frame{}, errors.New("destination validation fields are invalid")
			}
			frame.RequestID = requestID
			frame.DestinationArgs = args
		case "deliver":
			fields := []string{"version", "type", "request_id", "delivery_id", "input_plugin", "subscription_id", "destination_args", "context", "external_data", "permission_notice"}
			if err := protocol.ValidateFields(value, fields, fields...); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			deliveryID, deliveryOK := protocol.StringField(value, "delivery_id")
			inputPlugin, inputOK := protocol.StringField(value, "input_plugin")
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			args, argsOK := protocol.ObjectField(value, "destination_args")
			contextText, contextOK := protocol.StringField(value, "context")
			externalData, externalOK := protocol.BooleanField(value, "external_data")
			permissionNotice, noticeOK := protocol.StringField(value, "permission_notice")
			if !requestOK || requestID == "" || !deliveryOK || deliveryID == "" || !inputOK || inputPlugin == "" || !subscriptionOK || subscriptionID == "" || !argsOK || !contextOK || contextText == "" || !utf8.ValidString(contextText) || len([]byte(contextText)) > MaxContextBytes || !externalOK || !externalData || !noticeOK || permissionNotice != PermissionNotice {
				return Frame{}, errors.New("delivery request fields are invalid")
			}
			frame.RequestID = requestID
			frame.DeliveryID = deliveryID
			frame.InputPlugin = inputPlugin
			frame.SubscriptionID = subscriptionID
			frame.DestinationArgs = args
			frame.Context = contextText
			frame.ExternalData = &externalData
			frame.PermissionNotice = permissionNotice
		case "delivery_notice":
			fields := []string{"version", "type", "subscription_id", "event_id", "status"}
			if err := protocol.ValidateFields(value, fields, fields...); err != nil {
				return Frame{}, err
			}
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			eventID, eventOK := protocol.StringField(value, "event_id")
			status, statusOK := protocol.StringField(value, "status")
			if !subscriptionOK || subscriptionID == "" || !eventOK || !validEventID(eventID) || !statusOK || !validNoticeStatus(status) {
				return Frame{}, errors.New("delivery_notice fields are invalid")
			}
			frame.SubscriptionID, frame.EventID, frame.Status = subscriptionID, eventID, status
		case "checkpoint":
			fields := []string{"version", "type", "request_id", "subscription_id", "quiesce"}
			if err := protocol.ValidateFields(value, fields, fields...); err != nil {
				return Frame{}, err
			}
			requestID, requestOK := protocol.StringField(value, "request_id")
			subscriptionID, subscriptionOK := protocol.StringField(value, "subscription_id")
			quiesce, quiesceOK := protocol.BooleanField(value, "quiesce")
			if !requestOK || requestID == "" || !subscriptionOK || subscriptionID == "" || !quiesceOK {
				return Frame{}, errors.New("checkpoint fields are invalid")
			}
			frame.RequestID, frame.SubscriptionID, frame.Quiesce = requestID, subscriptionID, quiesce
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
		"version": jsonvalue.NewNumber("2"),
		"type":    jsonvalue.NewString(frame.Type),
	}
	switch frame.Type {
	case "state_stream_watch", "state_stream_chunk", "state_stream_end", "checkpoint_stream_begin", "checkpoint_stream_chunk", "checkpoint_stream_end":
		encodeStreamFields(fields, frame)
	case "cancel_watch":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
	case "ready":
		fields["role"] = jsonvalue.NewString(frame.Role)
		if frame.Capabilities != nil {
			values := make([]*jsonvalue.Value, 0, len(frame.Capabilities))
			for _, capability := range frame.Capabilities {
				values = append(values, jsonvalue.NewString(capability))
			}
			fields["capabilities"] = jsonvalue.NewArray(values...)
		}
	case "activate_watch":
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		if frame.Generation != "" {
			fields["generation"] = jsonvalue.NewString(frame.Generation)
		}
	case "resume_watch":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["generation"] = jsonvalue.NewString(frame.Generation)
		fields["confirmation_position"] = jsonvalue.NewString(frame.ConfirmationPosition)
	case "watch":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["watch_args"] = frame.WatchArgs
	case "state_watch":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["watch_args"] = frame.WatchArgs
		fields["state"] = frame.State
	case "watch_result", "destination_result", "resume_result", "cancel_result":
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
	case "state_event":
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["event_id"] = jsonvalue.NewString(frame.EventID)
		fields["context"] = jsonvalue.NewString(frame.Context)
		if frame.Generation != "" {
			fields["generation"] = jsonvalue.NewString(frame.Generation)
		}
	case "validate_destination":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["destination_args"] = frame.DestinationArgs
	case "deliver":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["delivery_id"] = jsonvalue.NewString(frame.DeliveryID)
		fields["input_plugin"] = jsonvalue.NewString(frame.InputPlugin)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["destination_args"] = frame.DestinationArgs
		fields["context"] = jsonvalue.NewString(frame.Context)
		external := false
		if frame.ExternalData != nil {
			external = *frame.ExternalData
		}
		fields["external_data"] = jsonvalue.NewBoolean(external)
		fields["permission_notice"] = jsonvalue.NewString(frame.PermissionNotice)
	case "delivery_notice":
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["event_id"] = jsonvalue.NewString(frame.EventID)
		fields["status"] = jsonvalue.NewString(frame.Status)
	case "checkpoint":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["quiesce"] = jsonvalue.NewBoolean(frame.Quiesce)
	case "delivery_result":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["status"] = jsonvalue.NewString(frame.Status)
		if frame.Error != "" {
			fields["error"] = jsonvalue.NewString(frame.Error)
		}
	case "checkpoint_result":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		if frame.Accepted != nil {
			fields["accepted"] = jsonvalue.NewBoolean(*frame.Accepted)
		}
		if frame.State != nil {
			fields["state"] = frame.State
		}
		if frame.Error != "" {
			fields["error"] = jsonvalue.NewString(frame.Error)
		}
	default:
		return nil, fmt.Errorf("unsupported frame type %q", frame.Type)
	}
	value := jsonvalue.NewObject(fields)
	if _, err := Decode(value, directionFor(frame.Type)); err != nil {
		return nil, err
	}
	payload := value.Marshal()
	if err := validatePayloadSize(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func directionFor(typeName string) Direction {
	switch typeName {
	case "ready", "watch_result", "event", "state_event", "destination_result", "delivery_result", "checkpoint_result", "resume_result", "cancel_result", "checkpoint_stream_begin", "checkpoint_stream_chunk", "checkpoint_stream_end":
		return PluginToDaemon
	default:
		return DaemonToPlugin
	}
}

// ValidateStateEnvelope checks only the public envelope shape and encoded-size
// limit. The trigger remains responsible for interpreting schema and payload.
func ValidateStateEnvelope(state *jsonvalue.Value) error {
	fields := []string{"schema", "payload"}
	if err := protocol.ValidateFields(state, fields, fields...); err != nil {
		return errors.New("state envelope fields are invalid")
	}
	schema, schemaOK := protocol.StringField(state, "schema")
	_, payloadOK := protocol.ObjectField(state, "payload")
	if !schemaOK || schema == "" || !utf8.ValidString(schema) || len([]byte(schema)) > MaxRuntimeSchemaBytes || !payloadOK {
		return errors.New("state envelope values are invalid")
	}
	if len(state.Marshal()) > MaxRuntimeStateBytes {
		return fmt.Errorf("state envelope exceeds %d bytes", MaxRuntimeStateBytes)
	}
	return nil
}

func validEventID(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func validContext(value string) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= MaxContextBytes
}

func validNoticeStatus(value string) bool {
	return value == "accepted" || value == "failed" || value == "unknown" || value == "discarded"
}

// ParsePayload validates one complete JSON payload received through Session.
// Callers may discard a malformed bounded payload and receive the next message.
func ParsePayload(payload []byte, direction Direction) (Frame, error) {
	if err := validatePayloadSize(payload); err != nil {
		return Frame{}, err
	}
	value, err := jsonvalue.Parse(payload, MaxPayloadBytes)
	if err != nil {
		return Frame{}, err
	}
	return Decode(value, direction)
}
