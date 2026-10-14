package domain

import "github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"

type (
	SubscriptionID string
	DeliveryID     string
	PluginRole     string
)

const (
	InputRole  PluginRole = "input"
	OutputRole PluginRole = "output"
)

type Subscription struct {
	ID              SubscriptionID
	InputPlugin     string
	OutputPlugin    string
	WatchArgs       *jsonvalue.Value
	DestinationArgs *jsonvalue.Value
	Active          bool
}

type Event struct {
	ID              DeliveryID
	InputPlugin     string
	OutputPlugin    string
	SubscriptionID  SubscriptionID
	DestinationArgs *jsonvalue.Value
	Context         string
}

type DeliveryOutcome string

const (
	DeliveryAccepted DeliveryOutcome = "accepted"
	DeliveryFailed   DeliveryOutcome = "failed"
	DeliveryUnknown  DeliveryOutcome = "unknown"
)
