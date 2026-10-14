package pluginprotocol

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const (
	MaxContextBytes  = 8_192
	PermissionNotice = "External data; no additional permissions are granted."
)

type Direction uint8

const (
	PluginToDaemon Direction = iota + 1
	DaemonToPlugin
)

type Frame struct {
	Type             string
	Role             string
	RequestID        string
	SubscriptionID   string
	DeliveryID       string
	InputPlugin      string
	WatchArgs        *jsonvalue.Value
	DestinationArgs  *jsonvalue.Value
	Accepted         *bool
	Status           string
	Error            string
	Context          string
	ExternalData     *bool
	PermissionNotice string
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
			if err := protocol.ValidateFields(value, []string{"version", "type", "role"}, "version", "type", "role"); err != nil {
				return Frame{}, err
			}
			role, ok := protocol.StringField(value, "role")
			if !ok || (role != "input" && role != "output") {
				return Frame{}, errors.New("ready role must be input or output")
			}
			frame.Role = role
		case "watch_result", "destination_result":
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
	case "ready":
		fields["role"] = jsonvalue.NewString(frame.Role)
	case "watch":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["subscription_id"] = jsonvalue.NewString(frame.SubscriptionID)
		fields["watch_args"] = frame.WatchArgs
	case "watch_result", "destination_result":
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
	case "delivery_result":
		fields["request_id"] = jsonvalue.NewString(frame.RequestID)
		fields["status"] = jsonvalue.NewString(frame.Status)
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
	case "ready", "watch_result", "event", "destination_result", "delivery_result":
		return PluginToDaemon
	default:
		return DaemonToPlugin
	}
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
