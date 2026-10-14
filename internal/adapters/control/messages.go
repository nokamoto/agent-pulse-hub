package control

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxErrorMessageBytes = 1_024

type RegistrationRequest struct {
	InputPlugin     string
	WatchArgs       *jsonvalue.Value
	OutputPlugin    string
	DestinationArgs *jsonvalue.Value
}

type RegistrationResponse struct {
	Success        bool
	SubscriptionID string
	Code           string
	Message        string
}

type controlRequest struct {
	operation    string
	registration RegistrationRequest
}

func parseRequest(frame []byte) (controlRequest, *hub.Error) {
	value, err := protocol.ParseFrame(frame)
	if err != nil {
		return controlRequest{}, &hub.Error{Code: "invalid_json", Message: "Control request is not valid JSON."}
	}
	if value.Kind() != jsonvalue.Object {
		return controlRequest{}, &hub.Error{Code: "invalid_request", Message: "Control request must be a JSON object."}
	}
	version, ok := protocol.NumberField(value, "version")
	if !ok || version != "1" {
		return controlRequest{}, &hub.Error{Code: "unsupported_version", Message: "Control protocol version is missing or unsupported."}
	}
	operation, ok := protocol.StringField(value, "op")
	if !ok || (operation != "register" && operation != "stop") {
		return controlRequest{}, &hub.Error{Code: "invalid_request", Message: "Control operation is invalid."}
	}
	if operation == "stop" {
		if err := protocol.ValidateFields(value, []string{"version", "op"}, "version", "op"); err != nil {
			return controlRequest{}, &hub.Error{Code: "invalid_request", Message: "Control request fields are invalid."}
		}
		return controlRequest{operation: operation}, nil
	}
	registration, requestErr := registrationFields(value)
	return controlRequest{operation: operation, registration: registration}, requestErr
}

func registrationFields(value *jsonvalue.Value) (RegistrationRequest, *hub.Error) {
	if err := protocol.ValidateFields(value,
		[]string{"version", "op", "input_plugin", "watch_args", "output_plugin", "destination_args"},
		"version", "op", "input_plugin", "watch_args", "output_plugin", "destination_args"); err != nil {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Control request fields are invalid."}
	}
	inputPlugin, inputOK := protocol.StringField(value, "input_plugin")
	outputPlugin, outputOK := protocol.StringField(value, "output_plugin")
	if !inputOK || inputPlugin == "" || !outputOK || outputPlugin == "" {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Input and output plugin names are required."}
	}
	watchArgs, watchOK := protocol.ObjectField(value, "watch_args")
	if !watchOK {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Watch arguments must be a JSON object."}
	}
	destinationArgs, destinationOK := protocol.ObjectField(value, "destination_args")
	if !destinationOK {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Destination arguments must be a JSON object."}
	}
	return RegistrationRequest{InputPlugin: inputPlugin, WatchArgs: watchArgs, OutputPlugin: outputPlugin, DestinationArgs: destinationArgs}, nil
}

func ParseRegistrationRequest(frame []byte) (RegistrationRequest, *hub.Error) {
	request, err := parseRequest(frame)
	if err != nil {
		return RegistrationRequest{}, err
	}
	if request.operation != "register" {
		return RegistrationRequest{}, &hub.Error{Code: "invalid_request", Message: "Control operation is invalid."}
	}
	return request.registration, nil
}

func EncodeSuccess(subscriptionID string) ([]byte, error) {
	return protocol.EncodeFrame(jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"ok":              jsonvalue.NewBoolean(true),
		"subscription_id": jsonvalue.NewString(subscriptionID),
	}))
}

func EncodeError(code, message string) ([]byte, error) {
	if code == "" || !utf8.ValidString(code) {
		return nil, errors.New("valid control error code is required")
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
	if !ok {
		return parseErrorResponse(value)
	}
	if err := protocol.ValidateFields(value, []string{"ok", "subscription_id"}, "ok", "subscription_id"); err != nil {
		return RegistrationResponse{}, errors.New("successful control response fields are invalid")
	}
	id, valid := protocol.StringField(value, "subscription_id")
	if !valid || id == "" {
		return RegistrationResponse{}, errors.New("successful control response has no subscription identifier")
	}
	return RegistrationResponse{Success: true, SubscriptionID: id}, nil
}

func parseErrorResponse(value *jsonvalue.Value) (RegistrationResponse, error) {
	if err := protocol.ValidateFields(value, []string{"ok", "code", "message"}, "ok", "code", "message"); err != nil {
		return RegistrationResponse{}, errors.New("error control response fields are invalid")
	}
	code, codeOK := protocol.StringField(value, "code")
	message, messageOK := protocol.StringField(value, "message")
	if !codeOK || code == "" || !messageOK || message == "" || len(message) > MaxErrorMessageBytes {
		return RegistrationResponse{}, errors.New("error control response is invalid")
	}
	return RegistrationResponse{Code: code, Message: message}, nil
}

func parseStopResponse(frame []byte) (RegistrationResponse, error) {
	value, err := protocol.ParseFrame(frame)
	if err != nil {
		return RegistrationResponse{}, err
	}
	ok, valid := protocol.BooleanField(value, "ok")
	if !valid {
		return RegistrationResponse{}, errors.New("control response has no boolean ok field")
	}
	if !ok {
		return parseErrorResponse(value)
	}
	if err := protocol.ValidateFields(value, []string{"ok"}, "ok"); err != nil {
		return RegistrationResponse{}, errors.New("successful stop response fields are invalid")
	}
	return RegistrationResponse{Success: true}, nil
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
