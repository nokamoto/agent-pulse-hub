package hub

import (
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

const (
	rewaitReceiptLimit       = 1024
	rewaitWindowMillis int64 = 300_000
)

// RewaitPluginPort is the optional control extension for runtime-state v2.
type RewaitPluginPort interface {
	ActivateGeneration(context.Context, domain.SubscriptionID, string) error
	ResumeWatch(context.Context, ResumeWatchRequest) error
}

type ResumeWatchRequest struct {
	RequestID            string
	SubscriptionID       domain.SubscriptionID
	Generation           string
	ConfirmationPosition string
}

// PositionRejectedError reports a completed negative preparation result.
type PositionRejectedError struct{}

func (*PositionRejectedError) Error() string { return "confirmation position was rejected" }

type notificationWait struct {
	generation     string
	armed          bool
	outstanding    bool
	preparing      bool
	checkpointBusy bool
}

type rewaitReceipt struct {
	subscriptionID string
	positionDigest [32]byte
	timestamp      int64
	done           chan struct{}
	result         *Error
}

// ValidateRewaitOperationID checks the canonical syntax without consulting time.
func ValidateRewaitOperationID(id string) bool {
	_, ok := rewaitOperationMillis(id)
	return ok
}

func rewaitOperationMillis(id string) (int64, bool) {
	timestamp, nonce, found := strings.Cut(id, ":")
	if !found || len(nonce) != 32 {
		return 0, false
	}
	for _, c := range nonce {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(timestamp, 10, 64)
	return value, err == nil && value >= 0 && strconv.FormatInt(value, 10) == timestamp
}

// Rewait prepares and commits one explicit intent. Its receipt survives a lost
// client response within this Session; disconnecting the client does not retry it.
func (h *Hub) Rewait(subscriptionID, operationID, confirmationPosition string) *Error {
	stamp, valid := rewaitOperationMillis(operationID)
	if !valid || subscriptionID == "" || confirmationPosition == "" || !utf8.ValidString(subscriptionID) || !utf8.ValidString(confirmationPosition) || len(confirmationPosition) > 4096 {
		return &Error{Code: "invalid_request", Message: "Rewait requires valid identifiers and a position of at most 4096 UTF-8 bytes."}
	}
	digest := sha256.Sum256([]byte(confirmationPosition))
	h.mu.Lock()
	now := h.rewaitNow().UnixMilli()
	if stamp <= h.sessionStartMillis || stamp > now || now-stamp > rewaitWindowMillis {
		h.mu.Unlock()
		return &Error{Code: "operation_expired", Message: "Operation ID is outside this daemon Session or its five-minute validity window; inspect the current wait before creating a new intent."}
	}
	if receipt := h.rewaitReceipts[operationID]; receipt != nil {
		if receipt.subscriptionID != subscriptionID || receipt.positionDigest != digest {
			h.mu.Unlock()
			return &Error{Code: "operation_conflict", Message: "Operation ID was already used with another subscription or position."}
		}
		h.mu.Unlock()
		<-receipt.done
		return receipt.result
	}
	id := domain.SubscriptionID(subscriptionID)
	registration, exists := h.registrations[id]
	if !exists || !registration.Active {
		h.mu.Unlock()
		return &Error{Code: "unknown_subscription", Message: "No retained active subscription has this ID."}
	}
	input, output := h.plugins[registration.InputPlugin], h.plugins[registration.OutputPlugin]
	if h.stopping || !h.admissionOpen || input == nil || !input.available || output == nil || !output.available {
		h.mu.Unlock()
		return &Error{Code: "plugin_unavailable", Message: "The daemon or a subscription plugin is unavailable."}
	}
	port, supported := input.port.(RewaitPluginPort)
	if !input.capabilities["rewait_v1"] || !supported {
		h.mu.Unlock()
		return &Error{Code: "rewait_unsupported", Message: "This subscription's input does not support repeated waiting."}
	}
	wait := h.notificationWaits[id]
	if wait == nil || wait.armed {
		h.mu.Unlock()
		return &Error{Code: "wait_active", Message: "The current notification wait is still active."}
	}
	if wait.outstanding || wait.preparing || wait.checkpointBusy {
		h.mu.Unlock()
		return &Error{Code: "rewait_busy", Message: "Wait for the preceding delivery notice, checkpoint, or rewait to finish, then explicitly retry this operation ID."}
	}
	for key, receipt := range h.rewaitReceipts {
		if now-receipt.timestamp > rewaitWindowMillis {
			delete(h.rewaitReceipts, key)
		}
	}
	if len(h.rewaitReceipts) >= rewaitReceiptLimit {
		h.mu.Unlock()
		return &Error{Code: "resource_exhausted", Message: "Rewait receipt capacity is full; retry after old operation IDs expire."}
	}
	generation, generationErr := newID()
	requestID, requestErr := newID()
	if generationErr != nil || requestErr != nil {
		h.mu.Unlock()
		return &Error{Code: "resource_exhausted", Message: "Rewait control identifiers could not be created."}
	}
	receipt := &rewaitReceipt{subscriptionID: subscriptionID, positionDigest: digest, timestamp: stamp, done: make(chan struct{})}
	h.rewaitReceipts[operationID] = receipt
	wait.preparing = true
	h.rewaitWait.Add(1)
	inputPort, outputPort := input.port, output.port
	h.mu.Unlock()
	defer h.rewaitWait.Done()

	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	err := port.ResumeWatch(ctx, ResumeWatchRequest{RequestID: requestID, SubscriptionID: id, Generation: generation, ConfirmationPosition: confirmationPosition})
	cancel()
	h.mu.Lock()
	var rejected *PositionRejectedError
	if err != nil {
		if errors.As(err, &rejected) {
			receipt.result = &Error{Code: "position_rejected", Message: "The input rejected this confirmation position; inspect retained continuity and supply a compatible position."}
		} else {
			receipt.result = &Error{Code: "plugin_unavailable", Message: "Rewait preparation could not be confirmed; its result is uncertain. Inspect the daemon before submitting another intent."}
		}
		wait.preparing = false
		close(receipt.done)
		stopping := h.stopping
		h.mu.Unlock()
		if !errors.As(err, &rejected) && !stopping {
			h.pluginUnavailable(registration.InputPlugin, inputPort, errors.New("rewait preparation failed"))
		}
		return receipt.result
	}
	input, output = h.plugins[registration.InputPlugin], h.plugins[registration.OutputPlugin]
	if h.stopping || input == nil || !input.available || input.port != inputPort || output == nil || !output.available || output.port != outputPort {
		receipt.result = &Error{Code: "plugin_unavailable", Message: "The daemon or a subscription plugin stopped before rewait commit."}
		wait.preparing = false
		close(receipt.done)
		h.mu.Unlock()
		return receipt.result
	}
	// This installation is the commit point. The preparation flag fences
	// checkpoints until its matching grant has entered the host stream.
	wait.generation, wait.armed = generation, true
	close(receipt.done)
	grantCtx, grantCancel := context.WithTimeout(h.ctx, activationDeadline)
	// Starting the grant under lifecycle serialization makes stop and commit
	// ordered. The transport must observe cancellation before sending on stop.
	h.mu.Unlock()
	err = port.ActivateGeneration(grantCtx, id, generation)
	grantCancel()
	h.mu.Lock()
	wait.preparing = false
	stopping := h.stopping
	h.mu.Unlock()
	if err != nil && !stopping {
		h.pluginUnavailable(registration.InputPlugin, inputPort, errors.New("committed rewait activation failed"))
	}
	return nil
}
