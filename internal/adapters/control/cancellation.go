package control

import (
	"context"
	"errors"

	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func ValidSubscriptionID(id string) bool { return hub.ValidSubscriptionID(id) }

func Unsubscribe(ctx context.Context, address, subscriptionID string) (RegistrationResponse, []byte, error) {
	if !ValidSubscriptionID(subscriptionID) {
		return RegistrationResponse{}, nil, errors.New("a canonical lowercase subscription UUID is required")
	}
	request := jsonvalue.NewObject(map[string]*jsonvalue.Value{
		"version": jsonvalue.NewNumber("1"), "op": jsonvalue.NewString("unsubscribe"),
		"subscription_id": jsonvalue.NewString(subscriptionID),
	})
	response, wire, err := exchange(ctx, address, request, ParseRegistrationResponse)
	if err == nil && response.SubscriptionID != subscriptionID {
		return RegistrationResponse{}, nil, &UncertainError{Reason: "the daemon response subscription ID did not match the request"}
	}
	return response, wire, err
}
