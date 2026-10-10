package controlpipe

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxErrorMessageBytes = 1_024

type RegistrationRequest struct {
	Plugin    string
	SessionID string
	WatchArgs *jsonvalue.Value
}

type RegistrationResponse struct {
	Success        bool
	SubscriptionID string
	Code           string
	Message        string
}

func ParseRegistrationRequest(frame []byte) (RegistrationRequest, *hub.Error) {
	value, err := protocol.ParseFrame(frame)
	if err != nil {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_json", Message: "Control request is not valid JSON."}
	}
	if value.Kind() != jsonvalue.Object {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Control request must be a JSON object."}
	}
	version, ok := protocol.NumberField(value, "version")
	if !ok || version != "1" {
		return RegistrationRequest{}, &hub.Error{Code: "unsupported_version", Message: "Control protocol version is missing or unsupported."}
	}
	if err := protocol.ValidateFields(value, []string{"version", "op", "plugin", "watch_args"}, "version", "op", "plugin", "session_id", "watch_args"); err != nil {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Control request fields are invalid."}
	}
	operation, ok := protocol.StringField(value, "op")
	if !ok || operation != "register" {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Control operation is invalid."}
	}
	pluginName, ok := protocol.StringField(value, "plugin")
	if !ok || pluginName == "" {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Plugin name is required."}
	}
	sessionID, ok := protocol.StringField(value, "session_id")
	if !ok || !domain.IsUUID(sessionID) {
		return RegistrationRequest{}, &hub.Error{Code: "missing_session", Message: "A valid session UUID is required."}
	}
	watchArgs, ok := protocol.ObjectField(value, "watch_args")
	if !ok {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Watch arguments must be a JSON object."}
	}
	return RegistrationRequest{Plugin: pluginName, SessionID: sessionID, WatchArgs: watchArgs}, nil
}

func EncodeSuccess(subscriptionID string) ([]byte, error) {
	return protocol.EncodeFrame(jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"ok":              jsonvalue.NewBoolean(true),
		"subscription_id": jsonvalue.NewString(subscriptionID),
	}))
}

func EncodeError(code, message string) ([]byte, error) {
	if code == "" {
		return nil, errors.New("control error code is required")
	}
	if !utf8.ValidString(message) {
		message = "Control request failed."
	}
	message = boundUTF8(message, MaxErrorMessageBytes)
	if message == "" {
		message = "Control request failed."
	}
	return protocol.EncodeFrame(jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"ok":      jsonvalue.NewBoolean(false),
		"code":    jsonvalue.NewString(code),
		"message": jsonvalue.NewString(message),
	}))
}

func EncodeHubError(err *hub.Error) ([]byte, error) {
	if err == nil {
		return nil, errors.New("control error is required")
	}
	return EncodeError(err.Code, err.Message)
}

func ParseRegistrationResponse(frame []byte) (RegistrationResponse, error) {
	value, err := protocol.ParseFrame(frame)
	if err != nil {
		return RegistrationResponse{}, fmt.Errorf("invalid control response JSON: %w", err)
	}
	ok, valid := protocol.BooleanField(value, "ok")
	if !valid {
		return RegistrationResponse{}, errors.New("control response has no boolean ok field")
	}
	if ok {
		if err := protocol.ValidateFields(value, []string{"ok", "subscription_id"}, "ok", "subscription_id"); err != nil {
			return RegistrationResponse{}, errors.New("successful control response fields are invalid")
		}
		id, valid := protocol.StringField(value, "subscription_id")
		if !valid || id == "" {
			return RegistrationResponse{}, errors.New("successful control response has no subscription identifier")
		}
		return RegistrationResponse{Success: true, SubscriptionID: id}, nil
	}
	if err := protocol.ValidateFields(value, []string{"ok", "code", "message"}, "ok", "code", "message"); err != nil {
		return RegistrationResponse{}, errors.New("error control response fields are invalid")
	}
	code, codeOK := protocol.StringField(value, "code")
	message, messageOK := protocol.StringField(value, "message")
	if !codeOK || code == "" || !messageOK || message == "" || len([]byte(message)) > MaxErrorMessageBytes {
		return RegistrationResponse{}, errors.New("error control response is invalid")
	}
	return RegistrationResponse{Success: false, Code: code, Message: message}, nil
}

func boundUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
