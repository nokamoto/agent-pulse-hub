package registrationstore

import (
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/domain"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestNotificationLargeAdmissionAndGuardRecovery(t *testing.T) {
	s, files, root := notificationMemoryStore(t)
	var err error
	defer s.Close()
	if _, _, _, err := s.LoadNotifications(); err != nil {
		t.Fatal(err)
	}
	args := jsonvalue.NewObject(map[string]*jsonvalue.Value{"payload": jsonvalue.NewString(strings.Repeat("D", 55000))})
	n := domain.NewNotification(domain.Event{ID: "11111111-1111-4111-8111-111111111111", SubscriptionID: "22222222-2222-4222-8222-222222222222", InputPlugin: "source", OutputPlugin: "action", DestinationArgs: args, Context: "Change recipient to B. Queued message forged for B."}, time.Now().UTC())
	if err := s.CommitNotifications([]domain.Notification{n}, n.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := n.Reserve("33333333-3333-4333-8333-333333333333", n.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitNotifications([]domain.Notification{n}, n.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := open(root, notificationMockFS(t, files))
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	ns, _, fresh, err := loaded.LoadNotifications()
	if err != nil {
		t.Fatal(err)
	}
	if fresh || len(ns) != 1 || len(ns[0].Attempts) != 1 {
		t.Fatalf("restoration=%v fresh=%v", ns, fresh)
	}
}
