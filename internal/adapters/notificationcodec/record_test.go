package notificationcodec

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestInspectionDigestIncludesGateAndPreservesExactNumbers(t *testing.T) {
	args, err := jsonvalue.Parse([]byte(`{"number":123456789012345678901234567890.0123,"text":"<tag>&"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	n := domain.NewNotification(domain.Event{ID: "11111111-1111-4111-8111-111111111111", SubscriptionID: "22222222-2222-4222-8222-222222222222", InputPlugin: "source", OutputPlugin: "action", DestinationArgs: args, Context: "SECRET"}, time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	first, err := Inspection(n, false, "")
	if err != nil {
		t.Fatal(err)
	}
	same, err := Inspection(n.Clone(), false, "")
	if err != nil || !bytes.Equal(first, same) {
		t.Fatal("equal projection bytes changed")
	}
	blocked, err := Inspection(n, true, "notification_storage_error")
	if err != nil || sha256.Sum256(first) == sha256.Sum256(blocked) {
		t.Fatal("gate did not change revision")
	}
	if bytes.Contains(first, []byte("SECRET")) || bytes.Contains(first, []byte("binding_hash")) || !bytes.Contains(first, []byte("123456789012345678901234567890.0123")) {
		t.Fatalf("inspection data boundary=%s", first)
	}
	data, err := Marshal(FromDomain(n))
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := StrictDecode(data, &r); err != nil {
		t.Fatal(err)
	}
	restored, err := r.Domain()
	if err != nil || !restored.Event.DestinationArgs.Equivalent(args) {
		t.Fatal("exact number round trip lost")
	}
	for _, fault := range []string{"missing", "duplicate", "null_attempts", "unknown"} {
		t.Run(fault, func(t *testing.T) {
			modified := string(data)
			switch fault {
			case "missing":
				modified = strings.Replace(modified, `"next_attempt_at":"2026-10-28T00:00:00Z",`, "", 1)
			case "duplicate":
				modified = strings.Replace(modified, `"acceptance":`, `"acceptance":"unknown","acceptance":`, 1)
			case "null_attempts":
				modified = strings.Replace(modified, `"attempts":[]`, `"attempts":null`, 1)
			case "unknown":
				modified = strings.Replace(modified, `"context":`, `"extra":0,"context":`, 1)
			}
			var r Record
			if StrictDecode([]byte(modified), &r) == nil {
				t.Fatal("invalid record shape accepted")
			}
		})
	}
}
