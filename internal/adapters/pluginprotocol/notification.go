package pluginprotocol

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const NotificationCapability = "notification_delivery_v1"

func notificationUUID(value string) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func decodeNotification(value *jsonvalue.Value, kind string) (Frame, error) {
	f := Frame{Type: kind}
	required := []string{"version", "type", "request_id", "delivery_id"}
	allowed := append(append([]string{}, required...), "receipt")
	if kind == "notification_result" {
		required = append(required, "status")
		allowed = append(allowed, "status", "reason", "message", "retryable", "retry_after_ms")
	}
	if err := protocol.ValidateFields(value, required, allowed...); err != nil {
		return f, err
	}
	f.RequestID, _ = protocol.StringField(value, "request_id")
	f.DeliveryID, _ = protocol.StringField(value, "delivery_id")
	if !notificationUUID(f.RequestID) || !notificationUUID(f.DeliveryID) {
		return f, errors.New("notification identifiers must be canonical non-nil UUIDs")
	}
	if kind == "notification_result" {
		f.Status, _ = protocol.StringField(value, "status")
	} else {
		f.Status = "accepted"
	}
	if f.Status == "accepted" {
		for _, name := range []string{"reason", "message", "retryable", "retry_after_ms"} {
			if _, ok := value.Get(name); ok {
				return f, errors.New("accepted evidence contains failure fields")
			}
		}
		if v, ok := value.Get("receipt"); ok {
			s, valid := jsonvalue.ParseString(v)
			if !valid || s == "" || len(s) > 1024 {
				return f, errors.New("invalid notification receipt")
			}
			f.Receipt = &s
		}
		return f, nil
	}
	if f.Status != "unsubmitted" && f.Status != "rejected" && f.Status != "unknown" {
		return f, errors.New("invalid notification status")
	}
	if _, ok := value.Get("receipt"); ok {
		return f, errors.New("nonaccepted result contains receipt")
	}
	f.Reason, _ = protocol.StringField(value, "reason")
	f.Message, _ = protocol.StringField(value, "message")
	if len(f.Reason) < 1 || len(f.Reason) > 128 || f.Message == "" || len(f.Message) > 1024 || !utf8.ValidString(f.Message) {
		return f, errors.New("invalid notification reason or message")
	}
	for _, c := range f.Reason {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return f, errors.New("invalid notification reason token")
		}
	}
	r, hasRetry := value.Get("retryable")
	a, hasAfter := value.Get("retry_after_ms")
	if f.Status != "unsubmitted" {
		if hasRetry || hasAfter {
			return f, errors.New("submitted result contains retry guidance")
		}
		return f, nil
	}
	b, valid := r.Bool()
	if !hasRetry || !valid {
		return f, errors.New("unsubmitted result requires retryable boolean")
	}
	f.Retryable = &b
	if hasAfter {
		token, valid := a.Text()
		n, err := strconv.Atoi(token)
		if !b || !valid || a.Kind() != jsonvalue.Number || err != nil || strconv.Itoa(n) != token || n < 0 || n > 60000 {
			return f, errors.New("invalid retry_after_ms")
		}
		f.RetryAfterMS = &n
	}
	return f, nil
}

func encodeNotification(f Frame) map[string]*jsonvalue.Value {
	fields := map[string]*jsonvalue.Value{"version": jsonvalue.NewNumber("2"), "type": jsonvalue.NewString(f.Type), "request_id": jsonvalue.NewString(f.RequestID), "delivery_id": jsonvalue.NewString(f.DeliveryID)}
	if f.Type == "notification_result" {
		fields["status"] = jsonvalue.NewString(f.Status)
	}
	if f.Reason != "" {
		fields["reason"] = jsonvalue.NewString(f.Reason)
	}
	if f.Message != "" {
		fields["message"] = jsonvalue.NewString(f.Message)
	}
	if f.Receipt != nil {
		fields["receipt"] = jsonvalue.NewString(*f.Receipt)
	}
	if f.Retryable != nil {
		fields["retryable"] = jsonvalue.NewBoolean(*f.Retryable)
	}
	if f.RetryAfterMS != nil {
		fields["retry_after_ms"] = jsonvalue.NewNumber(strconv.Itoa(*f.RetryAfterMS))
	}
	return fields
}
