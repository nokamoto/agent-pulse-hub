//go:build integration

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

var mvpEnv *mvpCaseEnv

var _ = BeforeSuite(func() {
	mvpEnv = prepareMVPSuite(GinkgoT())
})

var _ = AfterSuite(func() {
	mvpEnv.cleanup()
})

var _ = Describe("MVP input/output routing command acceptance", Serial, func() {
	It("rejects truncated configuration and missing input or output executables before startup", Label("case:MVP-V01-INVALID-CONFIG"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		truncated := filepath.Join(root, "truncated.json")
		if err := os.WriteFile(truncated, []byte(`{"plugins":`), 0o600); err != nil {
			t.Fatalf("write truncated config: %v", err)
		}
		result := runMVPCommand(t, 5*time.Second, mvpEnv.hub, "daemon", "--config", truncated, "--address", freeMVPAddress(t))
		if result.code() != 1 || !strings.Contains(result.stderr, "parse daemon configuration") {
			t.Fatalf("truncated config result: code=%d stderr=%q", result.code(), result.stderr)
		}

		outputCompanion := mvpEnv.pythonPlugin(t, root, "output-companion", "output")
		missingInput := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{
			{Name: "missing-input", Role: "input", Executable: filepath.Join(root, "missing-input"), Args: []string{}},
			outputCompanion,
		}})
		result = runMVPCommand(t, 5*time.Second, mvpEnv.hub, "daemon", "--config", missingInput, "--address", freeMVPAddress(t))
		if result.code() != 1 || !strings.Contains(result.stderr, "executable is unavailable") {
			t.Fatalf("missing input result: code=%d stderr=%q", result.code(), result.stderr)
		}
		inputCompanion := mvpEnv.pythonPlugin(t, root, "input-companion", "input")
		missingOutput := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{
			inputCompanion,
			{Name: "missing-output", Role: "output", Executable: filepath.Join(root, "missing-output"), Args: []string{}},
		}})
		result = runMVPCommand(t, 5*time.Second, mvpEnv.hub, "daemon", "--config", missingOutput, "--address", freeMVPAddress(t))
		if result.code() != 1 || !strings.Contains(result.stderr, "executable is unavailable") {
			t.Fatalf("missing output result: code=%d stderr=%q", result.code(), result.stderr)
		}
		for _, journal := range []string{
			filepath.Join(root, "output-companion", "journal.jsonl"),
			filepath.Join(root, "input-companion", "journal.jsonl"),
		} {
			if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid configuration launched companion plugin: %s: %v", journal, err)
			}
		}
	})

	It("activates a non-Go input/output registration only after both argument results", Label("case:MVP-V02-REGISTER"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input := mvpEnv.pythonPlugin(t, root, "P", "input")
		output := mvpEnv.pythonPlugin(t, root, "O", "output")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output}})
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemon(t, root, config, address, 2)
		DeferCleanup(daemon.cleanup)
		inputJournal := filepath.Join(root, "P", "journal.jsonl")
		outputJournal := filepath.Join(root, "O", "journal.jsonl")

		rejected := mvpEnv.register(t, 5*time.Second, address, "P", "O", map[string]any{}, map[string]any{"reject": true})
		response := parseMVPResponse(t, rejected)
		if rejected.code() != 1 || response["code"] != "destination_rejected" {
			t.Fatalf("destination rejection = code %d response %v stderr %q", rejected.code(), response, rejected.stderr)
		}
		if len(mvpRowsOfKind(inputJournal, "watch")) != 0 {
			t.Fatalf("input received a watch after output rejection: %v", mvpRowsOfKind(inputJournal, "watch"))
		}

		pendingCLI := mvpEnv.newCase(t)
		watchArgs := map[string]any{"gate": "watch-open", "immediate": "V02 immediate event"}
		destinationArgs := map[string]any{"gate": "destination-open"}
		watchPath := writeMVPObject(t, pendingCLI, "watch.json", watchArgs)
		destinationPath := writeMVPObject(t, pendingCLI, "destination.json", destinationArgs)
		async := startMVPCommand(t, mvpEnv.hub, "register", "--input-plugin", "P", "--watch-args-file", watchPath,
			"--output-plugin", "O", "--destination-args-file", destinationPath, "--address", address)
		awaitMVPJournal(t, outputJournal, "validate_destination", 2, 3*time.Second)
		if !waitMVP(t, 2*time.Second, func() bool {
			for _, row := range mvpRowsOfKind(outputJournal, "validate_destination") {
				frame, _ := row["frame"].(map[string]any)
				args, _ := frame["destination_args"].(map[string]any)
				if args["gate"] == "destination-open" {
					return true
				}
			}
			return false
		}) {
			t.Fatalf("gated destination request was not observed: %v", readMVPJournal(outputJournal))
		}
		if len(mvpRowsOfKind(inputJournal, "watch")) != 0 {
			t.Fatalf("input watch preceded destination acceptance: %v", mvpRowsOfKind(inputJournal, "watch"))
		}
		if result, ok := mvpAsyncResult(async); ok {
			t.Fatalf("registration completed while destination result was held: %+v", result)
		}
		if err := os.WriteFile(filepath.Join(root, "O", "destination-open"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		awaitMVPJournal(t, inputJournal, "watch", 1, 3*time.Second)
		if result, ok := mvpAsyncResult(async); ok {
			t.Fatalf("registration completed while watch result was held: %+v", result)
		}
		if err := os.WriteFile(filepath.Join(root, "P", "watch-open"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		accepted := awaitMVPAsync(t, async, 4*time.Second)
		if accepted.code() != 0 {
			t.Fatalf("accepted registration: code=%d stdout=%q stderr=%q", accepted.code(), accepted.stdout, accepted.stderr)
		}
		acceptedResponse := parseMVPResponse(t, accepted)
		acceptedID, ok := acceptedResponse["subscription_id"].(string)
		if !ok || acceptedID == "" {
			t.Fatalf("accepted response has no subscription ID: %v", acceptedResponse)
		}
		deliveries := awaitMVPJournal(t, outputJournal, "deliver", 1, 3*time.Second)
		frame, _ := deliveries[0]["frame"].(map[string]any)
		actualDestination, _ := frame["destination_args"].(map[string]any)
		if frame["subscription_id"] != acceptedID || frame["input_plugin"] != "P" || frame["context"] != "V02 immediate event" || actualDestination["gate"] != "destination-open" {
			t.Fatalf("immediate event changed the accepted registration or destination: %v", frame)
		}

		watchRejected := mvpEnv.register(t, 5*time.Second, address, "P", "O", map[string]any{"reject": true, "immediate": "must be discarded"}, map[string]any{})
		watchResponse := parseMVPResponse(t, watchRejected)
		if watchRejected.code() != 1 || watchResponse["code"] != "watch_rejected" {
			t.Fatalf("input rejection = code %d response %v", watchRejected.code(), watchResponse)
		}
		if !waitMVP(t, 2*time.Second, func() bool { return countMVPLog(daemon.logPath, "event_rejected") > 0 }) {
			t.Fatalf("event from rejected watch was not rejected: %s", readMVPFile(daemon.logPath))
		}
		if len(mvpRowsOfKind(outputJournal, "deliver")) != 1 {
			t.Fatalf("rejected watch caused a delivery: %v", mvpRowsOfKind(outputJournal, "deliver"))
		}
	})

	It("routes a normal model-input trigger to model-output destination A after an idle interval", Label("case:MVP-V03-ROUTE"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		trigger := filepath.Join(root, "trigger.txt")
		destinationA := filepath.Join(root, "destination-a.jsonl")
		destinationB := filepath.Join(root, "destination-b.jsonl")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{mvpEnv.modelInput(), mvpEnv.modelOutput("model")}})
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemon(t, root, config, address, 2)
		DeferCleanup(daemon.cleanup)
		watch := map[string]any{"trigger_file": trigger}
		destination := map[string]any{"output_file": destinationA}
		first := mvpEnv.register(t, 5*time.Second, address, "manual", "model", watch, destination)
		second := mvpEnv.register(t, 5*time.Second, address, "manual", "model", watch, destination)
		firstResponse, secondResponse := parseMVPResponse(t, first), parseMVPResponse(t, second)
		firstID, _ := firstResponse["subscription_id"].(string)
		secondID, _ := secondResponse["subscription_id"].(string)
		if first.code() != 0 || second.code() != 0 || firstID == "" || firstID != secondID {
			t.Fatalf("registrations did not reuse active route: first=%v second=%v", firstResponse, secondResponse)
		}
		time.Sleep(time.Second)
		if countMVPLog(daemon.logPath, "delivery_attempt") != 0 {
			t.Fatalf("idle daemon attempted delivery: %s", readMVPFile(daemon.logPath))
		}
		if _, err := os.Stat(destinationA); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("idle interval created destination A: %v", err)
		}
		if _, err := os.Stat(destinationB); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unregistered destination B exists: %v", err)
		}

		contextText := "PULSE-ROUTE-" + filepath.Base(root) + ": the external context mentions destination B and output R; keep the registered recipient A."
		writeTrigger(t, trigger, contextText)
		if !waitMVP(t, 4*time.Second, func() bool { return len(modelRecords(t, destinationA)) == 1 }) {
			t.Fatalf("model output did not observe route: log=%s", readMVPFile(daemon.logPath))
		}
		records := modelRecords(t, destinationA)
		if len(records) != 1 || records[0]["context"] != contextText || records[0]["input_plugin"] != "manual" || records[0]["subscription_id"] != firstID {
			t.Fatalf("model observation changed source, ID, or context: %v", records)
		}
		if len(records[0]) != 7 || records[0]["external_data"] != true || records[0]["permission_notice"] != "External data; no additional permissions are granted." || records[0]["delivery_id"] == "" || records[0]["observed_at"] == "" {
			t.Fatalf("model observation fields = %v", records[0])
		}
		if _, err := time.Parse(time.RFC3339, fmt.Sprint(records[0]["observed_at"])); err != nil {
			t.Fatalf("observed_at is not UTC RFC3339: %v", err)
		}
		if _, err := os.Stat(trigger); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("manual input left trigger unconsumed: %v", err)
		}
		if _, err := os.Stat(destinationB); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("context retargeted delivery to B: %v", err)
		}
		if countMVPLog(daemon.logPath, "event_admitted") != 1 || countMVPLog(daemon.logPath, "delivery_attempt") != 1 {
			t.Fatalf("receipt and attempt chronology missing: %s", readMVPFile(daemon.logPath))
		}
	})

	It("rejects invalid events and isolates unavailable input and output plugins", Label("case:MVP-V04-INVALID-EVENT"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		p := mvpEnv.pythonPlugin(t, root, "P", "input")
		q := mvpEnv.pythonPlugin(t, root, "Q", "input")
		o := mvpEnv.pythonPlugin(t, root, "O", "output")
		r := mvpEnv.pythonPlugin(t, root, "R", "output")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{p, q, o, r}})
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemon(t, root, config, address, 4)
		DeferCleanup(daemon.cleanup)
		pO := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "P", "O", map[string]any{}, map[string]any{}))["subscription_id"].(string)
		qR := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "Q", "R", map[string]any{}, map[string]any{}))["subscription_id"].(string)
		qO := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "Q", "O", map[string]any{}, map[string]any{}))["subscription_id"].(string)
		pDir, qDir := filepath.Join(root, "P"), filepath.Join(root, "Q")
		writeFixtureCommand(t, pDir, 1, map[string]any{"raw": "{not-json"})
		writeFixtureEvent(t, pDir, 2, "unknown-subscription", "unknown")
		writeFixtureEvent(t, pDir, 3, qR, "foreign")
		writeFixtureEvent(t, pDir, 4, pO, "PULSE-valid-P")
		oJournal, rJournal := filepath.Join(root, "O", "journal.jsonl"), filepath.Join(root, "R", "journal.jsonl")
		oRows := awaitMVPJournal(t, oJournal, "deliver", 1, 4*time.Second)
		if frame, _ := oRows[0]["frame"].(map[string]any); frame["context"] != "PULSE-valid-P" || frame["subscription_id"] != pO {
			t.Fatalf("invalid or foreign event reached output O: %v", frame)
		}
		time.Sleep(100 * time.Millisecond)
		if len(mvpRowsOfKind(oJournal, "deliver")) != 1 {
			t.Fatalf("malformed/unknown/foreign event caused output work: %v", mvpRowsOfKind(oJournal, "deliver"))
		}
		writeFixtureCommand(t, pDir, 5, map[string]any{"exit": true})
		if !waitMVP(t, 3*time.Second, func() bool { return hasMVPLogPlugin(daemon.logPath, "plugin_unavailable", "P") }) {
			t.Fatalf("input P exit was not reported: %s", readMVPFile(daemon.logPath))
		}
		failedInput := mvpEnv.register(t, 5*time.Second, address, "P", "R", map[string]any{"new": true}, map[string]any{})
		if parseMVPResponse(t, failedInput)["code"] != "plugin_unavailable" {
			t.Fatalf("failed input accepted registration: %s", failedInput.stdout)
		}
		writeFixtureEvent(t, qDir, 1, qR, "PULSE-Q-survives-input-P-exit")
		awaitMVPJournal(t, rJournal, "deliver", 1, 3*time.Second)
		writeFixtureCommand(t, filepath.Join(root, "O"), 1, map[string]any{"exit": true})
		if !waitMVP(t, 3*time.Second, func() bool { return hasMVPLogPlugin(daemon.logPath, "plugin_unavailable", "O") }) {
			t.Fatalf("output O exit was not reported: %s", readMVPFile(daemon.logPath))
		}
		writeFixtureEvent(t, qDir, 2, qO, "discard-through-O")
		writeFixtureEvent(t, qDir, 3, qR, "PULSE-Q-survives-output-O-exit")
		awaitMVPJournal(t, rJournal, "deliver", 2, 3*time.Second)
		if len(mvpRowsOfKind(oJournal, "deliver")) != 1 || len(mvpRowsOfKind(rJournal, "deliver")) != 2 {
			t.Fatalf("role failure isolation failed: O=%v R=%v", mvpRowsOfKind(oJournal, "deliver"), mvpRowsOfKind(rJournal, "deliver"))
		}
	})

	It("reports output rejection and unconfirmed acceptance including the actual delivery deadline without retry", Label("case:MVP-V04-DELIVERY-OUTCOME"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input := mvpEnv.pythonPlugin(t, root, "P", "input")
		failed := mvpEnv.pythonPlugin(t, root, "O-failed", "output")
		unknown := mvpEnv.pythonPlugin(t, root, "O-unknown", "output")
		hold := mvpEnv.pythonPlugin(t, root, "O-hold", "output")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, failed, unknown, hold}})
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemon(t, root, config, address, 4)
		DeferCleanup(daemon.cleanup)
		failedID := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "P", "O-failed", map[string]any{}, map[string]any{"status": "failed"}))["subscription_id"].(string)
		unknownID := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "P", "O-unknown", map[string]any{}, map[string]any{"status": "unknown"}))["subscription_id"].(string)
		holdID := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "P", "O-hold", map[string]any{}, map[string]any{"hold": true}))["subscription_id"].(string)
		inputDir := filepath.Join(root, "P")
		writeFixtureEvent(t, inputDir, 1, failedID, "failed once")
		writeFixtureEvent(t, inputDir, 2, unknownID, "unknown once")
		writeFixtureEvent(t, inputDir, 3, holdID, "held until deadline")
		awaitMVPJournal(t, filepath.Join(root, "O-failed", "journal.jsonl"), "deliver", 1, 4*time.Second)
		awaitMVPJournal(t, filepath.Join(root, "O-unknown", "journal.jsonl"), "deliver", 1, 4*time.Second)
		holdRows := awaitMVPJournal(t, filepath.Join(root, "O-hold", "journal.jsonl"), "deliver", 1, 4*time.Second)
		holdFrame, _ := holdRows[0]["frame"].(map[string]any)
		holdDeliveryID, _ := holdFrame["delivery_id"].(string)
		if !waitMVP(t, 34*time.Second, func() bool { return hasMVPLogDelivery(daemon.logPath, holdDeliveryID, "unknown") }) {
			t.Fatalf("actual 30 second output deadline was not reported: %s", readMVPFile(daemon.logPath))
		}
		elapsed, measured := mvpDeliveryElapsed(daemon.logPath, holdDeliveryID)
		if !measured {
			t.Fatalf("delivery attempt and completion timestamps were not logged: %s", readMVPFile(daemon.logPath))
		}
		if elapsed < 29*time.Second || elapsed >= 35*time.Second {
			t.Fatalf("delivery timeout elapsed=%s; want the actual 30-second deadline", elapsed)
		}
		if len(mvpRowsOfKind(filepath.Join(root, "O-failed", "journal.jsonl"), "deliver")) != 1 || len(mvpRowsOfKind(filepath.Join(root, "O-unknown", "journal.jsonl"), "deliver")) != 1 || len(mvpRowsOfKind(filepath.Join(root, "O-hold", "journal.jsonl"), "deliver")) != 1 {
			t.Fatalf("delivery was retried: failed=%d unknown=%d hold=%d", len(mvpRowsOfKind(filepath.Join(root, "O-failed", "journal.jsonl"), "deliver")), len(mvpRowsOfKind(filepath.Join(root, "O-unknown", "journal.jsonl"), "deliver")), len(mvpRowsOfKind(filepath.Join(root, "O-hold", "journal.jsonl"), "deliver")))
		}
		if !hasMVPLogDelivery(daemon.logPath, stringForRowID(mvpRowsOfKind(filepath.Join(root, "O-failed", "journal.jsonl"), "deliver")), "failed") || !hasMVPLogDelivery(daemon.logPath, stringForRowID(mvpRowsOfKind(filepath.Join(root, "O-unknown", "journal.jsonl"), "deliver")), "unknown") {
			t.Fatalf("explicit output outcomes were not distinguished: %s", readMVPFile(daemon.logPath))
		}
		if !waitMVP(t, 3*time.Second, func() bool { return hasMVPLogPlugin(daemon.logPath, "plugin_unavailable", "O-hold") }) {
			t.Fatalf("timed-out output was not invalidated: %s", readMVPFile(daemon.logPath))
		}
		daemon.stop(t, mvpEnv.hub)
		if !waitMVP(t, 2*time.Second, func() bool { return len(mvpRowsOfMessage(daemon.logPath, "plugin_process_exit")) >= 4 }) {
			t.Fatalf("standard cleanup did not observe every configured plugin process exit: %s", readMVPFile(daemon.logPath))
		}
		for _, row := range mvpRowsOfMessage(daemon.logPath, "plugin_process_exit") {
			if row["exited"] != true {
				t.Fatalf("configured plugin did not exit during cleanup: %v", row)
			}
		}
	})

	It("restores completed routes without replaying volatile work after normal restart", Label("case:MVP-V05-RESTART"), Pending, func() {
		// Governing design: docs/design/state-persistence.md; state-persistence
		// FR-006/AC-005 and MVP NFR-004/AC-005. Use the actual daemon/control and
		// existing controlled Python input/output plugins; no live services.
		// Isolate config/storage; await daemon_ready. Register accepted route S.
		// Hold one delivery result after its deliver journal entry (possibly sent
		// in-flight E1), then admit distinct E2 and await event_admitted so E2 is
		// queued behind E1. Separately gate a new registration P before watch
		// acceptance; journal barriers must establish that P is incomplete.
		// Normal stop, await daemon and every configured plugin_process_exit;
		// require E1's existing unknown-result observation, no E2 attempt and no
		// committed P record. Completed S remains saved with its original ID.
		// Remove consumed Python command-N.json stimulus files after stop, restart
		// sequence at 1, release fixture gates and restart same config/storage.
		// Await daemon_ready restored_registrations=1: no queued E2, retry of E1,
		// or resumed P acceptance/watch/delivery. Retain pre-stop journals and use
		// distinct contexts so fixture re-emission cannot masquerade as hub replay.
		// Emit fresh E3 on S without re-registering; one delivery with S and exact
		// E3 context succeeds. No old context appears after restart. Repeat normal
		// shutdown and observe all configured exits. Bound waits and cleanup.
		// Lifecycle unit tests cover pre/post-commit races without process matrices.
		// Budget: 10s case, shared warmed build <=20s, suite <=2 minutes.
	})

	It("deduplicates identical registrations while keeping output plugin and destination changes distinct", Label("case:MVP-V10-DUPLICATE"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input := mvpEnv.pythonPlugin(t, root, "P", "input")
		output := mvpEnv.pythonPlugin(t, root, "O", "output")
		otherOutput := mvpEnv.pythonPlugin(t, root, "R", "output")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output, otherOutput}})
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemon(t, root, config, address, 3)
		DeferCleanup(daemon.cleanup)
		inputDir, outputDir, otherOutputDir := filepath.Join(root, "P"), filepath.Join(root, "O"), filepath.Join(root, "R")
		watchArgs, destinationA := map[string]any{"gate": "watch-open"}, map[string]any{"destination": "A"}
		firstDir := mvpEnv.newCase(t)
		watchPath := writeMVPObject(t, firstDir, "watch.json", watchArgs)
		destinationPath := writeMVPObject(t, firstDir, "destination.json", destinationA)
		first := startMVPCommand(t, mvpEnv.hub, "register", "--input-plugin", "P", "--watch-args-file", watchPath,
			"--output-plugin", "O", "--destination-args-file", destinationPath, "--address", address)
		awaitMVPJournal(t, filepath.Join(inputDir, "journal.jsonl"), "watch", 1, 3*time.Second)
		if err := first.cmd.Process.Kill(); err != nil {
			t.Fatalf("interrupt first registration client: %v", err)
		}
		awaitMVPAsync(t, first, 2*time.Second)
		secondDir := mvpEnv.newCase(t)
		watchPath = writeMVPObject(t, secondDir, "watch.json", watchArgs)
		destinationPath = writeMVPObject(t, secondDir, "destination.json", destinationA)
		second := startMVPCommand(t, mvpEnv.hub, "register", "--input-plugin", "P", "--watch-args-file", watchPath,
			"--output-plugin", "O", "--destination-args-file", destinationPath, "--address", address)
		time.Sleep(100 * time.Millisecond)
		if len(mvpRowsOfKind(filepath.Join(inputDir, "journal.jsonl"), "watch")) != 1 {
			t.Fatalf("equivalent concurrent registration sent another watch: %v", readMVPJournal(filepath.Join(inputDir, "journal.jsonl")))
		}
		if err := os.WriteFile(filepath.Join(inputDir, "watch-open"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		secondResult := awaitMVPAsync(t, second, 4*time.Second)
		if secondResult.code() != 0 {
			t.Fatalf("shared registration result: %v %s", secondResult.err, secondResult.stderr)
		}
		firstID, _ := parseMVPResponse(t, secondResult)["subscription_id"].(string)
		repeated := mvpEnv.register(t, 5*time.Second, address, "P", "O", watchArgs, destinationA)
		repeatedID, _ := parseMVPResponse(t, repeated)["subscription_id"].(string)
		changedDestination := mvpEnv.register(t, 5*time.Second, address, "P", "O", watchArgs, map[string]any{"destination": "B"})
		destinationID, _ := parseMVPResponse(t, changedDestination)["subscription_id"].(string)
		changedOutput := mvpEnv.register(t, 5*time.Second, address, "P", "R", watchArgs, destinationA)
		outputID, _ := parseMVPResponse(t, changedOutput)["subscription_id"].(string)
		if firstID == "" || firstID != repeatedID || firstID == destinationID || firstID == outputID || destinationID == outputID {
			t.Fatalf("duplicate/recipient IDs = %q %q %q %q", firstID, repeatedID, destinationID, outputID)
		}
		if got := len(mvpRowsOfKind(filepath.Join(outputDir, "journal.jsonl"), "validate_destination")); got != 2 {
			t.Fatalf("output O validation count = %d; want one for A and one for B", got)
		}
		if got := len(mvpRowsOfKind(filepath.Join(otherOutputDir, "journal.jsonl"), "validate_destination")); got != 1 {
			t.Fatalf("output R validation count = %d; want one", got)
		}
		if got := len(mvpRowsOfKind(filepath.Join(inputDir, "journal.jsonl"), "watch")); got != 3 {
			t.Fatalf("watch count = %d; want one each for A, B, and R", got)
		}
		writeFixtureEvent(t, inputDir, 1, firstID, "PULSE-identical")
		writeFixtureEvent(t, inputDir, 2, firstID, "PULSE-identical")
		if !waitMVP(t, 4*time.Second, func() bool {
			return len(mvpRowsOfKind(filepath.Join(outputDir, "journal.jsonl"), "deliver")) >= 2
		}) {
			t.Fatalf("identical events did not reach the original output: input=%v O=%v R=%v daemon=%s",
				readMVPJournal(filepath.Join(inputDir, "journal.jsonl")),
				readMVPJournal(filepath.Join(outputDir, "journal.jsonl")),
				readMVPJournal(filepath.Join(otherOutputDir, "journal.jsonl")),
				readMVPFile(daemon.logPath))
		}
		deliveries := mvpRowsOfKind(filepath.Join(outputDir, "journal.jsonl"), "deliver")
		var ids, admittedIDs []string
		for _, row := range mvpRowsOfMessage(daemon.logPath, "event_admitted") {
			if row["subscription_id"] == firstID {
				admittedIDs = append(admittedIDs, fmt.Sprint(row["delivery_id"]))
			}
		}
		for _, row := range deliveries {
			frame, _ := row["frame"].(map[string]any)
			ids = append(ids, fmt.Sprint(frame["delivery_id"]))
			args, _ := frame["destination_args"].(map[string]any)
			if args["destination"] != "A" || frame["context"] != "PULSE-identical" || frame["subscription_id"] != firstID {
				t.Fatalf("original route was retargeted: %v", frame)
			}
		}
		if ids[0] == ids[1] || len(admittedIDs) != 2 || ids[0] != admittedIDs[0] || ids[1] != admittedIDs[1] || len(mvpRowsOfKind(filepath.Join(otherOutputDir, "journal.jsonl"), "deliver")) != 0 {
			t.Fatalf("identical events were merged, reordered, or redirected: admitted=%v delivered=%v R=%v", admittedIDs, ids, mvpRowsOfKind(filepath.Join(otherOutputDir, "journal.jsonl"), "deliver"))
		}
	})

	It("stops configured input and output plugin processes through the standard lifecycle", Label("case:MVP-V01-SHUTDOWN"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{mvpEnv.modelInput(), mvpEnv.modelOutput("model")}})
		address := freeMVPAddress(t)
		daemon := mvpEnv.startDaemon(t, root, config, address, 2)
		daemon.stop(t, mvpEnv.hub)
		if !waitMVP(t, 2*time.Second, func() bool { return countMVPLog(daemon.logPath, "plugin_process_exit") >= 2 }) {
			t.Fatalf("configured client exits were not logged: %s", readMVPFile(daemon.logPath))
		}
		seen := map[string]bool{}
		for _, row := range mvpRowsOfMessage(daemon.logPath, "plugin_process_exit") {
			name, _ := row["plugin"].(string)
			seen[name] = row["exited"] == true
		}
		if !seen["manual"] || !seen["model"] {
			t.Fatalf("configured process exit states = %v", seen)
		}
	})

	It("registers both roles locally and rejects a conflicting listener", Label("case:MVP-V08-LOCAL-CONTROL"), func() {
		t := GinkgoT()
		root := mvpEnv.newCase(t)
		input := mvpEnv.pythonPlugin(t, root, "P", "input")
		output := mvpEnv.pythonPlugin(t, root, "O", "output")
		config := writeMVPConfig(t, root, mvpConfig{Plugins: []mvpPluginConfig{input, output}})
		address := freeMVPAddress(t)
		first := mvpEnv.startDaemon(t, root, config, address, 2)
		DeferCleanup(first.cleanup)
		secondRoot := mvpEnv.newCase(t)
		secondInput := mvpEnv.pythonPlugin(t, secondRoot, "P2", "input")
		secondOutput := mvpEnv.pythonPlugin(t, secondRoot, "O2", "output")
		secondConfig := writeMVPConfig(t, secondRoot, mvpConfig{Plugins: []mvpPluginConfig{secondInput, secondOutput}})
		conflict := runMVPCommand(t, 5*time.Second, mvpEnv.hub, "daemon", "--config", secondConfig, "--address", address)
		if conflict.code() != 1 || !strings.Contains(conflict.stderr, "reserve control address") {
			t.Fatalf("conflicting daemon = code %d stderr %q", conflict.code(), conflict.stderr)
		}
		for _, path := range []string{filepath.Join(secondRoot, "P2", "journal.jsonl"), filepath.Join(secondRoot, "O2", "journal.jsonl")} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("conflicting daemon launched a plugin before bind: %s: %v", path, err)
			}
		}
		invalidAddress := runMVPCommand(t, 5*time.Second, mvpEnv.hub, "daemon", "--config", secondConfig, "--address", "0.0.0.0:47831")
		if invalidAddress.code() != 1 || !strings.Contains(invalidAddress.stderr, "literal IPv4 loopback") {
			t.Fatalf("non-loopback address was accepted: %q", invalidAddress.stderr)
		}
		if _, err := os.Stat(filepath.Join(secondRoot, "P2", "journal.jsonl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid address launched input plugin: %v", err)
		}
		id, _ := parseMVPResponse(t, mvpEnv.register(t, 5*time.Second, address, "P", "O", map[string]any{}, map[string]any{}))["subscription_id"].(string)
		if id == "" {
			t.Fatal("first daemon stopped accepting registrations after conflict")
		}
		writeFixtureEvent(t, filepath.Join(root, "P"), 1, id, "PULSE-local-control")
		awaitMVPJournal(t, filepath.Join(root, "O", "journal.jsonl"), "deliver", 1, 3*time.Second)
	})
})

func mvpAsyncResult(command *mvpAsyncCLI) (mvpCLIResult, bool) {
	select {
	case result := <-command.done:
		command.done <- result
		return result, true
	default:
		return mvpCLIResult{}, false
	}
}

func awaitMVPAsync(t mvpReporter, command *mvpAsyncCLI, timeout time.Duration) mvpCLIResult {
	t.Helper()
	select {
	case result := <-command.done:
		return result
	case <-time.After(timeout):
		t.Fatalf("asynchronous CLI exceeded %s", timeout)
		return mvpCLIResult{}
	}
}

func hasMVPLogPlugin(path, message, plugin string) bool {
	for _, row := range readMVPJournal(path) {
		if row["msg"] == message && row["plugin"] == plugin {
			return true
		}
	}
	return false
}

func hasMVPLogDelivery(path, deliveryID, outcome string) bool {
	for _, row := range readMVPJournal(path) {
		if row["msg"] == "delivery_result" && row["delivery_id"] == deliveryID && row["outcome"] == outcome {
			return true
		}
	}
	return false
}

func mvpDeliveryElapsed(path, deliveryID string) (time.Duration, bool) {
	var attemptedAt, completedAt time.Time
	for _, row := range readMVPJournal(path) {
		if row["delivery_id"] != deliveryID {
			continue
		}
		if row["msg"] == "delivery_attempt" {
			attemptedAt, _ = time.Parse(time.RFC3339Nano, fmt.Sprint(row["attempted_at"]))
		}
		if row["msg"] == "delivery_result" {
			completedAt, _ = time.Parse(time.RFC3339Nano, fmt.Sprint(row["completed_at"]))
		}
	}
	if attemptedAt.IsZero() || completedAt.IsZero() {
		return 0, false
	}
	return completedAt.Sub(attemptedAt), true
}

func stringForRowID(rows []map[string]any) string {
	if len(rows) == 0 {
		return ""
	}
	frame, _ := rows[0]["frame"].(map[string]any)
	return fmt.Sprint(frame["delivery_id"])
}
