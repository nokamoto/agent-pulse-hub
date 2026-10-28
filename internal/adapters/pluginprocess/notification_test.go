package pluginprocess

import (
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain"
)

func TestNotificationCorrelationsRejectForeignDuplicateAndLateEvidence(t *testing.T) {
	for _, variant := range []string{"matching", "wrong_delivery", "wrong_request", "no_unknown", "expired", "duplicate"} {
		t.Run(variant, func(t *testing.T) {
			p := New(Config{Name: "action", Role: domain.OutputRole}, nil)
			p.capabilities[pluginprotocol.NotificationCapability] = true
			calls := 0
			c := &notificationCorrelation{deliveryID: "notification", deadline: time.Now().Add(time.Hour), unknown: true, evidence: func(e domain.AttemptEvidence) {
				calls++
				if e.Status != "accepted" {
					t.Fatal("supplement is not acceptance")
				}
			}}
			p.notificationCorrelations = map[string]*notificationCorrelation{"attempt": c}
			f := pluginprotocol.Frame{Type: "notification_evidence", RequestID: "attempt", DeliveryID: "notification", Status: "accepted"}
			switch variant {
			case "wrong_delivery":
				f.DeliveryID = "foreign"
			case "wrong_request":
				f.RequestID = "foreign"
			case "no_unknown":
				c.unknown = false
			case "expired":
				c.deadline = time.Now().Add(-time.Second)
			}
			handled, callback := p.handleNotificationFrameLocked(f)
			if !handled {
				t.Fatal("supplement not consumed")
			}
			if callback != nil {
				callback()
			}
			if variant == "duplicate" {
				_, callback = p.handleNotificationFrameLocked(f)
				if callback != nil {
					callback()
				}
			}
			want := 0
			if variant == "matching" || variant == "duplicate" {
				want = 1
			}
			if calls != want {
				t.Fatalf("callbacks=%d want=%d", calls, want)
			}
		})
	}
}
