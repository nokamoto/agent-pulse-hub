package domain

import "github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"

type (
	SubscriptionID string
	DeliveryID     string
)

type Subscription struct {
	ID        SubscriptionID
	Plugin    string
	SessionID string
	WatchArgs *jsonvalue.Value
}

type Event struct {
	ID             DeliveryID
	Plugin         string
	SubscriptionID SubscriptionID
	SessionID      string
	Context        string
}

type DeliveryOutcome string

const (
	DeliveryAccepted DeliveryOutcome = "accepted"
	DeliveryFailed   DeliveryOutcome = "failed"
	DeliveryUnknown  DeliveryOutcome = "unknown"
)

func IsUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}
