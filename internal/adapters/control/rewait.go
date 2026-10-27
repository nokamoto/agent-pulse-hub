package control

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxPositionBytes = 4_096

// ValidOperationID checks syntax only; the daemon owns Session/time admission.
func ValidOperationID(id string) bool {
	return hub.ValidateRewaitOperationID(id)
}

func ValidPosition(position string) bool {
	return position != "" && utf8.ValidString(position) && len(position) <= MaxPositionBytes
}

func Rewait(ctx context.Context, address, subscriptionID, operationID, position string) (RegistrationResponse, []byte, error) {
	if subscriptionID == "" || !ValidOperationID(operationID) || !ValidPosition(position) {
		return RegistrationResponse{}, nil, errors.New("rewait identifiers or confirmation position are invalid")
	}
	request := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version": jsonvalue.NewNumber("1"), "op": jsonvalue.NewString("rewait"),
		"subscription_id":       jsonvalue.NewString(subscriptionID),
		"operation_id":          jsonvalue.NewString(operationID),
		"confirmation_position": jsonvalue.NewString(position),
	})
	response, wire, err := exchange(ctx, address, request, parseRewaitResponse)
	if err == nil && (response.SubscriptionID != subscriptionID || response.OperationID != operationID) {
		return RegistrationResponse{}, nil, &UncertainError{Reason: "the daemon response identifiers did not match the request"}
	}
	return response, wire, err
}
