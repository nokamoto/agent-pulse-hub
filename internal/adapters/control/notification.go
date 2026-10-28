package control

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/notificationcodec"
	"github.com/nokamoto/agent-pulse-hub/internal/adapters/protocol"
	"github.com/nokamoto/agent-pulse-hub/internal/application/hub"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func notificationInteger(v *jsonvalue.Value, key string, defaultValue, min, max int) (int, bool) {
	f, present := v.Get(key)
	if !present {
		return defaultValue, true
	}
	s, ok := f.Text()
	n, err := strconv.Atoi(s)
	return n, ok && f.Kind() == jsonvalue.Number && err == nil && strconv.Itoa(n) == s && n >= min && n <= max
}

func notificationRevision(s string) bool {
	if len(s) != 64 {
		return false
	}
	b, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(b) == s
}

func parseNotificationRequest(v *jsonvalue.Value, op string) (controlRequest, *hub.Error) {
	bad := func() (controlRequest, *hub.Error) {
		return controlRequest{}, &hub.Error{Code: "invalid_request", Message: "Notification inspection fields are invalid."}
	}
	r := controlRequest{operation: op}
	if op == "notifications" {
		fields := []string{"version", "op", "subscription_id"}
		if protocol.ValidateFields(v, fields, append(fields, "after", "limit")...) != nil {
			return bad()
		}
		r.subscriptionID, _ = protocol.StringField(v, "subscription_id")
		if !domain.ValidNotificationID(r.subscriptionID) {
			return bad()
		}
		if a, present := v.Get("after"); present {
			var ok bool
			r.after, ok = jsonvalue.ParseString(a)
			if !ok || !domain.ValidNotificationID(r.after) {
				return bad()
			}
		}
		var ok bool
		r.limit, ok = notificationInteger(v, "limit", 20, 1, 20)
		if !ok {
			return bad()
		}
	} else {
		fields := []string{"version", "op", "notification_id"}
		if protocol.ValidateFields(v, fields, append(fields, "offset", "revision")...) != nil {
			return bad()
		}
		r.notificationID, _ = protocol.StringField(v, "notification_id")
		if !domain.ValidNotificationID(r.notificationID) {
			return bad()
		}
		var ok bool
		r.offset, ok = notificationInteger(v, "offset", 0, 0, notificationcodec.MaxRecordBytes)
		if !ok {
			return bad()
		}
		if a, present := v.Get("revision"); present {
			r.revision, ok = jsonvalue.ParseString(a)
			if !ok || !notificationRevision(r.revision) {
				return bad()
			}
		} else if r.offset != 0 {
			return bad()
		}
	}
	return r, nil
}

func notificationResponse(service *hub.Hub, r controlRequest) ([]byte, *hub.Error) {
	if r.operation == "notifications" {
		items, next, err := service.ListNotifications(r.subscriptionID, r.after, r.limit)
		if err != nil {
			return nil, err
		}
		values := make([]*jsonvalue.Value, 0, len(items))
		for _, item := range items {
			n := item.Notification
			state, reason := n.RetryState, n.Reason
			if state == "scheduled" && item.Gate != "" {
				state = "blocked"
				reason = &item.Gate
			}
			reasonValue := jsonvalue.NewNull()
			if reason != nil {
				reasonValue = jsonvalue.NewString(*reason)
			}
			values = append(values, jsonvalue.NewObject(map[string]*jsonvalue.Value{"notification_id": jsonvalue.NewString(string(n.Event.ID)), "created_at": jsonvalue.NewString(n.CreatedAt.UTC().Format(time.RFC3339Nano)), "acceptance": jsonvalue.NewString(n.Acceptance), "retry_state": jsonvalue.NewString(state), "attempt_count": jsonvalue.NewNumber(strconv.Itoa(len(n.Attempts))), "reason": reasonValue}))
		}
		nextValue := jsonvalue.NewNull()
		if next != "" {
			nextValue = jsonvalue.NewString(next)
		}
		data, e := protocol.EncodeFrame(jsonvalue.NewObject(map[string]*jsonvalue.Value{"ok": jsonvalue.NewBoolean(true), "subscription_id": jsonvalue.NewString(r.subscriptionID), "notifications": jsonvalue.NewArray(values...), "next_after": nextValue}))
		if e != nil {
			return nil, &hub.Error{Code: "notification_storage_error", Message: "Could not encode notification inspection."}
		}
		return data, nil
	}
	n, blocked, gate, err := service.InspectNotification(r.notificationID)
	if err != nil {
		return nil, err
	}
	data, e := notificationcodec.Inspection(n, blocked, gate)
	if e != nil {
		return nil, &hub.Error{Code: "notification_storage_error", Message: "Could not encode notification inspection."}
	}
	hash := sha256.Sum256(data)
	revision := hex.EncodeToString(hash[:])
	if r.revision != "" && r.revision != revision {
		return nil, &hub.Error{Code: "notification_changed", Message: "Notification inspection changed; restart the read."}
	}
	if r.offset >= len(data) {
		return nil, &hub.Error{Code: "invalid_request", Message: "Notification offset is outside the payload."}
	}
	end := min(r.offset+16384, len(data))
	wire, e := protocol.EncodeFrame(jsonvalue.NewObject(map[string]*jsonvalue.Value{"ok": jsonvalue.NewBoolean(true), "notification_id": jsonvalue.NewString(r.notificationID), "revision": jsonvalue.NewString(revision), "offset": jsonvalue.NewNumber(strconv.Itoa(r.offset)), "total_bytes": jsonvalue.NewNumber(strconv.Itoa(len(data))), "data": jsonvalue.NewString(base64.StdEncoding.EncodeToString(data[r.offset:end]))}))
	if e != nil {
		return nil, &hub.Error{Code: "notification_storage_error", Message: "Could not encode notification chunk."}
	}
	return wire, nil
}

func notificationExchange(ctx context.Context, address string, request *jsonvalue.Value) (*jsonvalue.Value, []byte, error) {
	var value *jsonvalue.Value
	_, wire, err := exchange(ctx, address, request, func(data []byte) (RegistrationResponse, error) {
		var e error
		value, e = protocol.ParseFrame(data)
		if e != nil {
			return RegistrationResponse{}, e
		}
		ok, valid := protocol.BooleanField(value, "ok")
		if !valid {
			return RegistrationResponse{}, errors.New("invalid notification response")
		}
		if !ok {
			return parseErrorResponse(value)
		}
		return RegistrationResponse{Success: true}, nil
	})
	return value, wire, err
}

func Notifications(ctx context.Context, address, subscriptionID, after string, limit int) ([]byte, error) {
	fields := map[string]*jsonvalue.Value{"version": jsonvalue.NewNumber("1"), "op": jsonvalue.NewString("notifications"), "subscription_id": jsonvalue.NewString(subscriptionID), "limit": jsonvalue.NewNumber(strconv.Itoa(limit))}
	if after != "" {
		fields["after"] = jsonvalue.NewString(after)
	}
	request := jsonvalue.NewObject(fields)
	if _, e := parseNotificationRequest(request, "notifications"); e != nil {
		return nil, e
	}
	v, wire, err := notificationExchange(ctx, address, request)
	if err != nil {
		return nil, err
	}
	if protocol.ValidateFields(v, []string{"ok", "subscription_id", "notifications", "next_after"}, "ok", "subscription_id", "notifications", "next_after") != nil {
		return nil, errors.New("invalid notification list response")
	}
	id, _ := protocol.StringField(v, "subscription_id")
	items, _ := v.Get("notifications")
	next, _ := v.Get("next_after")
	if id != subscriptionID || items.Kind() != jsonvalue.Array || len(items.Items()) > limit {
		return nil, errors.New("invalid notification list binding")
	}
	previous := after
	for _, item := range items.Items() {
		keys := []string{"notification_id", "created_at", "acceptance", "retry_state", "attempt_count", "reason"}
		if protocol.ValidateFields(item, keys, keys...) != nil {
			return nil, errors.New("invalid notification summary")
		}
		id, _ := protocol.StringField(item, "notification_id")
		_, countOK := notificationInteger(item, "attempt_count", -1, 0, 3)
		if !domain.ValidNotificationID(id) || id <= previous || !countOK {
			return nil, errors.New("invalid notification summary identity")
		}
		previous = id
	}
	if next.Kind() != jsonvalue.Null {
		cursor, ok := jsonvalue.ParseString(next)
		if !ok || cursor != previous || len(items.Items()) != limit {
			return nil, errors.New("invalid notification cursor")
		}
	}
	return wire, nil
}

func Notification(ctx context.Context, address, id string) ([]byte, error) {
	if !domain.ValidNotificationID(id) {
		return nil, errors.New("invalid notification UUID")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	for restart := 0; restart < 2; restart++ {
		var assembled []byte
		revision := ""
		total := 0
		for {
			fields := map[string]*jsonvalue.Value{"version": jsonvalue.NewNumber("1"), "op": jsonvalue.NewString("notification"), "notification_id": jsonvalue.NewString(id), "offset": jsonvalue.NewNumber(strconv.Itoa(len(assembled)))}
			if revision != "" {
				fields["revision"] = jsonvalue.NewString(revision)
			}
			v, _, err := notificationExchange(ctx, address, jsonvalue.NewObject(fields))
			if err != nil {
				var de *DaemonError
				if errors.As(err, &de) && de.Response.Code == "notification_changed" && restart == 0 {
					break
				}
				return nil, err
			}
			keys := []string{"ok", "notification_id", "revision", "offset", "total_bytes", "data"}
			if protocol.ValidateFields(v, keys, keys...) != nil {
				return nil, errors.New("invalid notification chunk response")
			}
			actualID, _ := protocol.StringField(v, "notification_id")
			rev, _ := protocol.StringField(v, "revision")
			offset, offsetOK := notificationInteger(v, "offset", -1, 0, notificationcodec.MaxRecordBytes)
			size, sizeOK := notificationInteger(v, "total_bytes", -1, 1, notificationcodec.MaxRecordBytes)
			encoded, encodedOK := protocol.StringField(v, "data")
			chunk, e := base64.StdEncoding.Strict().DecodeString(encoded)
			if actualID != id || !notificationRevision(rev) || revision != "" && rev != revision || !offsetOK || offset != len(assembled) || !sizeOK || total != 0 && total != size || !encodedOK || e != nil || len(chunk) == 0 || len(chunk) > 16384 || len(assembled)+len(chunk) > size {
				return nil, errors.New("invalid notification chunk binding or bounds")
			}
			revision, total = rev, size
			assembled = append(assembled, chunk...)
			if len(assembled) == total {
				hash := sha256.Sum256(assembled)
				if hex.EncodeToString(hash[:]) != revision {
					return nil, errors.New("notification payload digest mismatch")
				}
				value, e := jsonvalue.Parse(assembled, notificationcodec.MaxRecordBytes)
				if e != nil {
					return nil, e
				}
				actual, _ := protocol.StringField(value, "notification_id")
				if actual != id {
					return nil, errors.New("notification inspection identity mismatch")
				}
				return append(assembled, '\n'), nil
			}
		}
	}
	return nil, errors.New("notification changed repeatedly")
}
