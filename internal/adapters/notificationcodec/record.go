package notificationcodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

const MaxRecordBytes = 131072

func Marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

type Attempt struct {
	ID           string     `json:"attempt_id"`
	Ordinal      int        `json:"ordinal"`
	ReservedAt   time.Time  `json:"reserved_at"`
	IssuedAt     *time.Time `json:"issued_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	Status       string     `json:"status"`
	Reason       *string    `json:"reason"`
	Message      *string    `json:"message"`
	Receipt      *string    `json:"receipt"`
	Retryable    *bool      `json:"retryable"`
	RetryAfterMS *int       `json:"retry_after_ms"`
}

type Record struct {
	ID              string          `json:"notification_id"`
	SubscriptionID  string          `json:"subscription_id"`
	InputPlugin     string          `json:"input_plugin"`
	OutputPlugin    string          `json:"output_plugin"`
	DestinationArgs json.RawMessage `json:"destination_args"`
	CreatedAt       time.Time       `json:"created_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Acceptance      string          `json:"acceptance"`
	RetryState      string          `json:"retry_state"`
	Reason          *string         `json:"reason"`
	NextAttemptAt   *time.Time      `json:"next_attempt_at"`
	Attempts        []Attempt       `json:"attempts"`
	Context         string          `json:"context"`
	BindingHash     string          `json:"binding_hash"`
}

func FromDomain(n domain.Notification) Record {
	r := Record{ID: string(n.Event.ID), SubscriptionID: string(n.Event.SubscriptionID), InputPlugin: n.Event.InputPlugin, OutputPlugin: n.Event.OutputPlugin, DestinationArgs: n.Event.DestinationArgs.Marshal(), CreatedAt: n.CreatedAt, ExpiresAt: n.ExpiresAt, Acceptance: n.Acceptance, RetryState: n.RetryState, Reason: n.Reason, NextAttemptAt: n.NextAttemptAt, Attempts: []Attempt{}, Context: n.Event.Context, BindingHash: n.BindingHash}
	for _, a := range n.Attempts {
		r.Attempts = append(r.Attempts, Attempt{a.ID, a.Ordinal, a.ReservedAt, a.IssuedAt, a.FinishedAt, a.Status, a.Reason, a.Message, a.Receipt, a.Retryable, a.RetryAfterMS})
	}
	return r
}

func (r Record) Domain() (domain.Notification, error) {
	args, err := jsonvalue.Parse(r.DestinationArgs, MaxRecordBytes)
	if err != nil || args.Kind() != jsonvalue.Object {
		return domain.Notification{}, errors.New("invalid saved destination")
	}
	n := domain.Notification{Event: domain.Event{ID: domain.DeliveryID(r.ID), SubscriptionID: domain.SubscriptionID(r.SubscriptionID), InputPlugin: r.InputPlugin, OutputPlugin: r.OutputPlugin, DestinationArgs: args, Context: r.Context}, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, Acceptance: r.Acceptance, RetryState: r.RetryState, Reason: r.Reason, NextAttemptAt: r.NextAttemptAt, BindingHash: r.BindingHash, Attempts: []domain.NotificationAttempt{}}
	for _, a := range r.Attempts {
		n.Attempts = append(n.Attempts, domain.NotificationAttempt{ID: a.ID, Ordinal: a.Ordinal, ReservedAt: a.ReservedAt, IssuedAt: a.IssuedAt, FinishedAt: a.FinishedAt, AttemptEvidence: domain.AttemptEvidence{Status: a.Status, Reason: a.Reason, Message: a.Message, Receipt: a.Receipt, Retryable: a.Retryable, RetryAfterMS: a.RetryAfterMS}})
	}
	if err := Validate(n); err != nil {
		return n, err
	}
	return n, nil
}

// StrictDecode rejects duplicate keys, invalid Unicode, unknown and missing fields.
// Decoding again into the explicit DTO retains exact numbers in RawMessage.
func StrictDecode(data []byte, target any, required ...string) error {
	v, err := jsonvalue.Parse(data, 134217728)
	if err != nil || v.Kind() != jsonvalue.Object {
		return errors.New("invalid strict JSON object")
	}
	for _, key := range required {
		if _, ok := v.Get(key); !ok {
			return errors.New("missing required field: " + key)
		}
	}
	if err := validateShape(v, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func validateShape(v *jsonvalue.Value, t reflect.Type) error {
	if t == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if v.Kind() == jsonvalue.Null {
			return nil
		}
		return validateShape(v, t.Elem())
	}
	if t == reflect.TypeFor[time.Time]() {
		s, ok := jsonvalue.ParseString(v)
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if !ok || err != nil || !strings.HasSuffix(s, "Z") || parsed.IsZero() {
			return errors.New("invalid UTC timestamp")
		}
		return nil
	}
	if t.Kind() == reflect.Struct {
		if v.Kind() != jsonvalue.Object {
			return errors.New("record is not an object")
		}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			if key == "" {
				key = field.Name
			}
			child, ok := v.Get(key)
			if !ok {
				return errors.New("missing required field: " + key)
			}
			if err := validateShape(child, field.Type); err != nil {
				return err
			}
		}
	}
	if t.Kind() == reflect.Slice {
		if v.Kind() != jsonvalue.Array {
			return errors.New("record array is missing or null")
		}
		for _, child := range v.Items() {
			if err := validateShape(child, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func Validate(n domain.Notification) error {
	if !domain.ValidNotificationID(string(n.Event.ID)) || !domain.ValidNotificationID(string(n.Event.SubscriptionID)) || n.Event.InputPlugin == "" || n.Event.OutputPlugin == "" || n.Event.Context == "" || len(n.Event.Context) > 8192 || n.CreatedAt.IsZero() || n.CreatedAt.Location() != time.UTC || !n.ExpiresAt.Equal(n.CreatedAt.Add(domain.NotificationRetention)) || n.BindingHash != domain.NotificationBinding(n.Event) || len(n.Attempts) > 3 {
		return errors.New("invalid notification binding or bounds")
	}
	if n.RetryState != "scheduled" && n.RetryState != "in_progress" && n.RetryState != "accepted" && n.RetryState != "permanent" && n.RetryState != "unknown" && n.RetryState != "exhausted" && n.RetryState != "cancelled" && n.RetryState != "expired" {
		return errors.New("invalid durable retry state")
	}
	if (n.RetryState == "scheduled") != (n.NextAttemptAt != nil) {
		return errors.New("retry due time is inconsistent")
	}
	if n.NextAttemptAt != nil && (!n.NextAttemptAt.Before(n.ExpiresAt) || n.NextAttemptAt.Before(n.CreatedAt)) {
		return errors.New("invalid retry due time")
	}
	ids := map[string]bool{}
	for i, a := range n.Attempts {
		if a.Ordinal != i+1 || !domain.ValidNotificationID(a.ID) || ids[a.ID] || a.ReservedAt.Before(n.CreatedAt) || !a.ReservedAt.Before(n.ExpiresAt) {
			return errors.New("invalid attempt identity or time")
		}
		ids[a.ID] = true
		if a.IssuedAt != nil && a.IssuedAt.Before(a.ReservedAt) {
			return errors.New("invalid issued time")
		}
		if a.Status == "in_progress" {
			if i != len(n.Attempts)-1 || a.FinishedAt != nil || a.Reason != nil || a.Message != nil || a.Receipt != nil || a.Retryable != nil || a.RetryAfterMS != nil {
				return errors.New("invalid reservation")
			}
		} else {
			if a.FinishedAt == nil || a.FinishedAt.Before(a.ReservedAt) {
				return errors.New("invalid finished time")
			}
			if err := a.AttemptEvidence.Validate(); err != nil {
				return err
			}
		}
		if i < len(n.Attempts)-1 && (a.Status != "unsubmitted" || a.Retryable == nil || !*a.Retryable) {
			return errors.New("retry follows possible submission")
		}
	}
	acceptance := "not_attempted"
	if len(n.Attempts) > 0 {
		acceptance = n.Attempts[len(n.Attempts)-1].Status
		if acceptance == "accepted" {
			acceptance = "queue_accepted"
		}
	}
	if n.Acceptance != acceptance {
		return errors.New("acceptance contradicts attempts")
	}
	if n.RetryState == "accepted" && acceptance != "queue_accepted" || n.RetryState == "unknown" && acceptance != "unknown" || n.RetryState == "in_progress" && acceptance != "in_progress" || n.RetryState == "exhausted" && (len(n.Attempts) != 3 || acceptance != "unsubmitted") {
		return errors.New("retry state contradicts acceptance")
	}
	if n.RetryState == "scheduled" {
		if len(n.Attempts) >= 3 {
			return errors.New("exhausted notification cannot be scheduled")
		}
		for _, a := range n.Attempts {
			if a.Status != "unsubmitted" || a.Retryable == nil || !*a.Retryable {
				return errors.New("unsafe scheduled retry")
			}
		}
		if len(n.Attempts) > 0 {
			a := n.Attempts[len(n.Attempts)-1]
			delay := 2 * time.Second
			if len(n.Attempts) == 2 {
				delay = 10 * time.Second
			}
			if a.RetryAfterMS != nil {
				delay = max(delay, time.Duration(*a.RetryAfterMS)*time.Millisecond)
			}
			if n.NextAttemptAt.Before(a.FinishedAt.Add(delay)) {
				return errors.New("scheduled retry violates minimum wait")
			}
		}
	}
	if n.RetryState == "permanent" {
		if len(n.Attempts) == 0 {
			return errors.New("permanent state has no attempt evidence")
		}
		a := n.Attempts[len(n.Attempts)-1]
		if a.Status != "rejected" && (a.Status != "unsubmitted" || a.Retryable == nil || *a.Retryable) {
			return errors.New("permanent state contradicts attempt evidence")
		}
	}
	if n.RetryState == "exhausted" && (!*n.Attempts[len(n.Attempts)-1].Retryable) {
		return errors.New("permanent failure cannot be exhausted")
	}
	data, err := Marshal(FromDomain(n))
	if err != nil || len(data) > MaxRecordBytes {
		return errors.New("notification exceeds record bound")
	}
	return nil
}

func Inspection(n domain.Notification, blocked bool, gate string) ([]byte, error) {
	r := FromDomain(n)
	data, err := Marshal(r)
	if err != nil {
		return nil, err
	}
	v, err := jsonvalue.Parse(data, MaxRecordBytes)
	if err != nil {
		return nil, err
	}
	fields := v.Fields()
	delete(fields, "context")
	delete(fields, "binding_hash")
	fields["storage_blocked"] = jsonvalue.NewBoolean(blocked)
	if n.RetryState == "scheduled" && gate != "" {
		fields["retry_state"] = jsonvalue.NewString("blocked")
		fields["reason"] = jsonvalue.NewString(gate)
	}
	payload := jsonvalue.NewObject(fields).Marshal()
	if len(payload) > MaxRecordBytes {
		return nil, errors.New("inspection exceeds record bound")
	}
	return payload, nil
}
