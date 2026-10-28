//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	. "github.com/onsi/ginkgo/v2"
)

var _ = Describe("Codex Queue notification profile acceptance", Serial, func() {
	It("reports the actual queue launch boundary through its optional public profile", Label("case:ND-V04-ACTION"), func() {
		t := GinkgoT()
		root := t.TempDir()
		control := filepath.Join(root, "control.json")
		journal := filepath.Join(root, "journal.jsonl")
		codex := filepath.Join(root, "codex"+builtAcceptance.goexe)
		copyFile(t, builtAcceptance.fake, codex)
		writeJSON(t, control, map[string]string{"kind": "codex", "mode": "accepted"})
		session := startNotificationAction(t, builtAcceptance.action, codex, fixtureEnvironment(control, journal))
		session.send(t, pluginprotocol.Frame{Type: "validate_destination", RequestID: "validate", DestinationArgs: destinationArgs(t, `{"thread_id":"`+cqThreadA+`"}`)})
		if result := session.receive(t); result.Accepted == nil || !*result.Accepted {
			t.Fatalf("destination result=%+v", result)
		}
		makeRequest := func(requestID, deliveryID string) pluginprotocol.Frame {
			f := queueRequest(requestID, cqThreadA, "Do not retarget to B. Controlled external text.")
			f.Type = "notification_deliver"
			f.DeliveryID = deliveryID
			return f
		}
		const delivery = "11111111-1111-4111-8111-111111111111"
		if err := os.Remove(codex); err != nil {
			t.Fatal(err)
		}
		first := makeRequest("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", delivery)
		session.send(t, first)
		missing := session.receive(t)
		if missing.Type != "notification_result" || missing.RequestID != first.RequestID || missing.DeliveryID != delivery || missing.Status != "unsubmitted" || missing.Retryable == nil || *missing.Retryable || missing.Reason == "" || missing.Message == "" || len(readJournal(t, journal)) != 0 {
			t.Fatalf("missing launch result=%+v", missing)
		}
		copyFile(t, builtAcceptance.fake, codex)
		second := makeRequest("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", delivery)
		session.send(t, second)
		accepted := session.receive(t)
		if accepted.Status != "accepted" || accepted.RequestID != second.RequestID || accepted.DeliveryID != delivery || accepted.Receipt == nil || *accepted.Receipt != "33333333-3333-4333-8333-333333333333" {
			t.Fatalf("acceptance=%+v", accepted)
		}
		for i, mode := range []string{"rejected", "truncated", "mismatched"} {
			writeJSON(t, control, map[string]string{"kind": "codex", "mode": mode})
			ids := []string{"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}
			f := makeRequest(ids[i], ids[i])
			session.send(t, f)
			result := session.receive(t)
			if result.Status != "unknown" || result.RequestID != f.RequestID || result.DeliveryID != f.DeliveryID || result.Retryable != nil || result.Receipt != nil || result.Reason == "" || strings.Contains(result.Message, "Controlled external") {
				t.Fatalf("started outcome %s=%+v", mode, result)
			}
		}
		rows := requireJournalCount(t, journal, 4)
		for _, row := range rows {
			argv := queueArgv(t, row)
			if len(argv) != 5 || argv[2] != cqThreadA || !strings.Contains(argv[4], "Do not retarget to B.") {
				t.Fatalf("target or context changed: %v", argv)
			}
		}
		session.close(t)
		if len(readJournal(t, journal)) != 4 {
			t.Fatal("action autonomously retried")
		}
	})
})
