//go:build integration && windows

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sys/windows"
)

var _ = Describe("MVP daemon and registration public behavior", Serial, func() {
	It("rejects representative invalid startup inputs before starting plugins", Label("case:MVP-V01-INVALID-CONFIG"), func() {
		c := newCaseRun(4*time.Second, 3)
		malformed := filepath.Join(c.root, "malformed.json")
		malformedData, err := json.Marshal(transcript{"codex_executable": harness.codex, "plugins": []transcript{c.plugin("fixture")}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(malformed, malformedData[:len(malformedData)-1], 0o600)).To(Succeed())
		missing := filepath.Join(c.root, "missing.json")
		writeJSON(missing, transcript{"codex_executable": harness.codex, "plugins": []transcript{{"name": "fixture", "executable": filepath.Join(c.root, "absent.exe"), "args": []string{}}}})
		for _, path := range []string{malformed, missing} {
			command := exec.Command(harness.daemon, "daemon", "--config", path)
			command.Env = childEnvironment("APH_FIXTURE_ROOT=" + c.root)
			var output, diagnostics bytes.Buffer
			command.Stdout = &output
			command.Stderr = &diagnostics
			err := c.runNegative(command, 4*time.Second, 1)
			Expect(err).To(HaveOccurred())
			Expect(command.ProcessState.ExitCode()).To(Equal(1))
			Expect(output.String()).To(BeEmpty())
			Expect(diagnostics.Len()).To(BeNumerically(">", 0))
			Expect(diagnostics.Len()).To(BeNumerically("<=", 1025))
			Expect(strings.Count(diagnostics.String(), "\n")).To(Equal(1))
		}
		Expect(c.calls()).To(BeEmpty())
		Expect(readRows(filepath.Join(c.root, "versions.jsonl"))).To(BeEmpty())
		Expect(c.watchRecords()).To(BeEmpty())
		ready, _ := filepath.Glob(filepath.Join(c.root, "*", "ready.json"))
		Expect(ready).To(BeEmpty())
	})
	It("activates a language-neutral watch only after acknowledgement", Label("case:MVP-V02-REGISTER"), func() {
		c := newCaseRun(5*time.Second, 5)
		c.start(c.plugin("fixture"))
		client := c.registerAsync("fixture", transcript{"immediate": true})
		Eventually(func() int { return len(c.watchRecords()) }, time.Second, 5*time.Millisecond).Should(Equal(1))
		watch := c.watchRecords()[0]
		Consistently(func() bool {
			select {
			case <-client.done:
				client.consumed = true
				return false
			default:
				return true
			}
		}, 75*time.Millisecond, 5*time.Millisecond).Should(BeTrue())
		Expect(countMessage(c.logs(), "subscription_active")).To(Equal(0))
		Expect(c.calls()).To(BeEmpty())
		c.ack()
		row := client.await()
		Expect(client.exit).To(Equal(0))
		Expect(client.diagnostics.String()).To(BeEmpty())
		Expect(row).To(HaveLen(2))
		Expect(row["ok"]).To(Equal(true))
		Expect(row["subscription_id"]).To(Equal(watch["subscription_id"]))
		results := c.awaitDeliveries(1)
		Expect(results[0]["outcome"]).To(Equal("accepted"))
		c.assertCall(0, row["subscription_id"].(string), "fixture", psContext)
		rejected := c.registerAsync("fixture", transcript{"reject": true})
		failure := rejected.await()
		Expect(rejected.exit).To(Equal(1))
		Expect(rejected.diagnostics.String()).To(BeEmpty())
		Expect(failure).To(Equal(transcript{"ok": false, "code": "watch_rejected", "message": "Plugin rejected the watch arguments."}))
		Expect(rejected.output.String()).To(Equal("{\"code\":\"watch_rejected\",\"message\":\"Plugin rejected the watch arguments.\",\"ok\":false}\n"))
		Expect(countMessage(c.logs(), "subscription_active")).To(Equal(1))
		Expect(c.calls()).To(HaveLen(1))
		harness.stop()
		Expect(readRows(filepath.Join(watch["directory"].(string), "shutdown.json"))).To(HaveLen(1))
	})
	It("routes an atomic manual trigger after an idle interval", Label("case:MVP-V03-ROUTE"), func() {
		c := newCaseRun(4*time.Second, 5)
		trigger := filepath.Join(c.root, "trigger.txt")
		c.start(transcript{"name": "manual", "executable": harness.manual, "args": []string{}})
		id := c.register("manual", transcript{"trigger_file": trigger})
		Expect(c.register("manual", transcript{"trigger_file": trigger})).To(Equal(id))
		idleStarted := time.Now()
		Consistently(func() int { return len(c.calls()) }, time.Second, 10*time.Millisecond).Should(Equal(0))
		idleElapsed := time.Since(idleStarted)
		Expect(countMessage(c.logs(), "delivery_attempt")).To(Equal(0))
		temporary := filepath.Join(c.root, "publish.tmp")
		Expect(os.WriteFile(temporary, []byte(routeContext), 0o600)).To(Succeed())
		Expect(os.Rename(temporary, trigger)).To(Succeed())
		results := c.awaitDeliveries(1)
		Expect(results[0]["outcome"]).To(Equal("accepted"))
		c.assertCall(0, id, "manual", routeContext)
		_, err := os.Stat(trigger)
		Expect(os.IsNotExist(err)).To(BeTrue())
		claimed, err := filepath.Glob(filepath.Join(c.root, ".agent-pulse-hub-claim-*"))
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(BeEmpty())
		var receipt, attempt time.Time
		for _, row := range c.logs() {
			if row["msg"] == "event_admitted" {
				receipt, err = time.Parse(time.RFC3339Nano, row["received_at"].(string))
				Expect(err).NotTo(HaveOccurred())
			}
			if row["msg"] == "delivery_attempt" {
				attempt, err = time.Parse(time.RFC3339Nano, row["attempted_at"].(string))
				Expect(err).NotTo(HaveOccurred())
			}
		}
		Expect(receipt.IsZero()).To(BeFalse())
		Expect(attempt.Before(receipt)).To(BeFalse())
		AddReportEntry("idle and route trace", transcript{"idle_ms": idleElapsed.Milliseconds(), "received_at": receipt, "attempted_at": attempt})
		for _, name := range []string{"run_windows.go", "path_windows.go"} {
			source, err := os.ReadFile(filepath.Join(harness.repository, "internal", "adapters", "manualplugin", name))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(source)).NotTo(ContainSubstring("os/exec"))
			Expect(string(source)).NotTo(ContainSubstring("internal/adapters/codex"))
			Expect(string(source)).NotTo(ContainSubstring("internal/application/hub"))
		}
		AddReportEntry("manual-plugin source review", "run_windows.go and path_windows.go have no subprocess, Codex adapter, or hub capability; their public frames carry only subscription and context")
	})
	It("recovers a malformed frame and isolates invalid or exited emitters", Label("case:MVP-V04-INVALID-EVENT"), func() {
		c := newCaseRun(5*time.Second, 6)
		c.ack()
		c.start(c.plugin("A"), c.plugin("B"))
		idA := c.register("A", transcript{"source": "A"})
		idB := c.register("B", transcript{"source": "B"})
		watchA, watchB := c.watch(idA), c.watch(idB)
		c.action(watchA, []string{"{malformed", eventLine(idA, "valid A"), eventLine("unknown-subscription", "unknown"), eventLine(idB, "foreign B")}, false)
		results := c.awaitDeliveries(1)
		Expect(results[0]["outcome"]).To(Equal("accepted"))
		c.assertCall(0, idA, "A", "valid A")
		Eventually(func() int { return countMessage(c.logs(), "event_rejected") }, time.Second, 5*time.Millisecond).Should(Equal(2))
		Expect(countMessage(c.logs(), "plugin_frame_rejected")).To(Equal(1))
		process := openProcess(fixturePID(watchA))
		defer windows.CloseHandle(process)
		c.action(watchA, nil, true)
		Eventually(func() int { return countMessage(c.logs(), "plugin_unavailable") }, time.Second, 5*time.Millisecond).Should(Equal(1))
		state, err := windows.WaitForSingleObject(process, 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(uint32(windows.WAIT_OBJECT_0)))
		client := c.registerAsync("A", transcript{"new": true})
		failure := client.await()
		Expect(client.exit).To(Equal(1))
		Expect(failure["code"]).To(Equal("plugin_unavailable"))
		newB := c.register("B", transcript{"source": "B-new"})
		Expect(newB).NotTo(Equal(idB))
		c.action(watchB, []string{eventLine(newB, "valid B")}, false)
		results = c.awaitDeliveries(2)
		Expect(results[1]["outcome"]).To(Equal("accepted"))
		c.assertCall(1, newB, "B", "valid B")
		Expect(countMessage(c.logs(), "delivery_attempt")).To(Equal(2))
	})
	It("terminates an uncertain delivery without retrying", Label("case:MVP-V04-DELIVERY-OUTCOME"), func() {
		c := newCaseRun(35*time.Second, 5)
		c.ack()
		c.start(c.plugin("fixture"))
		id := c.register("fixture", transcript{"source": "outcomes"})
		watch := c.watch(id)
		Expect(os.WriteFile(filepath.Join(c.root, "mode"), []byte("nonzero"), 0o600)).To(Succeed())
		c.action(watch, []string{eventLine(id, "nonzero")}, false)
		results := c.awaitDeliveries(1)
		Expect(results[0]["outcome"]).To(Equal("unknown"))
		Expect(results[0]["plugin"]).To(Equal("fixture"))
		Expect(results[0]["subscription_id"]).To(Equal(id))
		c.assertCall(0, id, "fixture", "nonzero")
		Expect(os.WriteFile(filepath.Join(c.root, "mode"), []byte("held"), 0o600)).To(Succeed())
		c.action(watch, []string{eventLine(id, "held")}, false)
		Eventually(func() int { return len(c.calls()) }, time.Second, 5*time.Millisecond).Should(Equal(2))
		call := c.calls()[1]
		process := openProcess(number(call, "pid"))
		defer windows.CloseHandle(process)
		invoked, err := time.Parse(time.RFC3339Nano, call["at"].(string))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() int { return countMessage(c.logs(), "delivery_result") }, 31*time.Second, 20*time.Millisecond).Should(Equal(2))
		state, err := windows.WaitForSingleObject(process, 1000)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(uint32(windows.WAIT_OBJECT_0)))
		Expect(time.Since(invoked)).To(BeNumerically(">=", 29*time.Second))
		Expect(time.Since(invoked)).To(BeNumerically("<", 32*time.Second))
		var timeout transcript
		for _, row := range c.logs() {
			if row["msg"] == "delivery_result" && row["delivery_id"] != results[0]["delivery_id"] {
				timeout = row
			}
		}
		Expect(timeout["outcome"]).To(Equal("unknown"))
		Expect(timeout["plugin"]).To(Equal("fixture"))
		Expect(timeout["subscription_id"]).To(Equal(id))
		Expect(timeout["detail"]).To(Equal("Codex queue command timed out."))
		Expect(countMessage(c.logs(), "delivery_attempt")).To(Equal(2))
		Expect(c.calls()).To(HaveLen(2))
		c.assertCall(1, id, "fixture", "held")
	})
	It("rejects stale watches after restarting in-memory state", Label("case:MVP-V05-RESTART"), func() {
		c := newCaseRun(4*time.Second, 5)
		c.ack()
		c.start(c.plugin("fixture"))
		old := c.register("fixture", transcript{"source": "restart"})
		oldWatch := c.watch(old)
		harness.stop()
		Expect(readRows(filepath.Join(oldWatch["directory"].(string), "shutdown.json"))).To(HaveLen(1))
		c.start(c.plugin("fixture"))
		fresh := c.register("fixture", transcript{"source": "restart"})
		Expect(fresh).NotTo(Equal(old))
		c.action(c.watch(fresh), []string{eventLine(old, "stale"), eventLine(fresh, "fresh")}, false)
		results := c.awaitDeliveries(1)
		Expect(results[0]["outcome"]).To(Equal("accepted"))
		c.assertCall(0, fresh, "fixture", "fresh")
		Expect(countMessage(c.logs(), "event_rejected")).To(Equal(1))
		Expect(countMessage(c.logs(), "delivery_attempt")).To(Equal(1))
	})
	It("deduplicates committed registration despite response loss and concurrent repeats", Label("case:MVP-V10-DUPLICATE"), func() {
		c := newCaseRun(6*time.Second, 6)
		c.start(c.plugin("fixture"))
		args := transcript{"source": "duplicate"}
		lost := rawRegister("fixture", args)
		Expect(lost.Close()).To(Succeed())
		Eventually(func() int { return len(c.watchRecords()) }, time.Second, 5*time.Millisecond).Should(Equal(1))
		watch := c.watchRecords()[0]
		first, second := rawRegister("fixture", args), rawRegister("fixture", args)
		type response struct {
			data []byte
			err  error
		}
		responses := make(chan response, 2)
		for _, connection := range []netControlConnection{first, second} {
			go func(connection netControlConnection) {
				data, err := bufio.NewReader(connection).ReadBytes('\n')
				_ = connection.Close()
				responses <- response{data, err}
			}(connection)
		}
		Consistently(func() int { return len(responses) }, 75*time.Millisecond, 5*time.Millisecond).Should(Equal(0))
		Expect(countMessage(c.logs(), "subscription_active")).To(Equal(0))
		Expect(c.watchRecords()).To(HaveLen(1))
		c.ack()
		for n := 0; n < 2; n++ {
			select {
			case result := <-responses:
				Expect(result.err).NotTo(HaveOccurred())
				var row transcript
				Expect(json.Unmarshal(result.data, &row)).To(Succeed())
				Expect(row).To(Equal(transcript{"ok": true, "subscription_id": watch["subscription_id"]}))
			case <-time.After(time.Second):
				Fail("concurrent registration response deadline")
			}
		}
		id := watch["subscription_id"].(string)
		Expect(c.register("fixture", args)).To(Equal(id))
		Expect(c.watchRecords()).To(HaveLen(1))
		c.action(watch, []string{eventLine(id, "identical"), eventLine(id, "identical")}, false)
		results := c.awaitDeliveries(2)
		Expect(results[0]["delivery_id"]).NotTo(Equal(results[1]["delivery_id"]))
		for _, result := range results {
			Expect(result["outcome"]).To(Equal("accepted"))
			Expect(result["subscription_id"]).To(Equal(id))
		}
		var admitted, attempted []string
		for _, row := range c.logs() {
			if row["msg"] == "event_admitted" {
				admitted = append(admitted, row["delivery_id"].(string))
			}
			if row["msg"] == "delivery_attempt" {
				attempted = append(attempted, row["delivery_id"].(string))
			}
		}
		Expect(admitted).To(HaveLen(2))
		Expect(attempted).To(Equal(admitted))
		Expect(c.calls()).To(HaveLen(2))
		c.assertCall(0, id, "fixture", "identical")
		c.assertCall(1, id, "fixture", "identical")
	})
	It("cleans the owned process tree after a normal Windows Ctrl+C", Label("case:MVP-V01-SHUTDOWN"), func() {
		c := newCaseRun(15*time.Second, 4)
		c.start(transcript{"name": "stubborn", "executable": harness.console, "args": []string{"stubborn", c.root}})
		stubborn := readRows(filepath.Join(c.root, "stubborn.json"))
		Expect(stubborn).To(HaveLen(1))
		Expect(readRows(filepath.Join(c.root, "leaf.json"))).To(HaveLen(1))
		plugin := openProcess(number(stubborn[0], "pid"))
		defer windows.CloseHandle(plugin)
		descendant := openProcess(number(stubborn[0], "descendant"))
		defer windows.CloseHandle(descendant)
		row := harness.stop()
		Expect(row["pids"].([]any)).To(HaveLen(2))
		Expect(number(row, "elapsed_ms")).To(BeNumerically(">=", 4900))
		Expect(number(row, "elapsed_ms")).To(BeNumerically("<", 7000))
		Expect(readRows(filepath.Join(c.root, "shutdown.json"))).To(HaveLen(1))
		for _, process := range []windows.Handle{plugin, descendant} {
			state, err := windows.WaitForSingleObject(process, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(state).To(Equal(uint32(windows.WAIT_OBJECT_0)))
		}
		Expect(countMessage(c.logs(), "registration_available")).To(Equal(1))
		Expect(c.calls()).To(BeEmpty())
		AddReportEntry("normal console signal and process tree", row)
	})
	It("enforces the current-user pipe policy and rejects a second daemon", Label("case:MVP-V08-SAME-USER"), func() {
		c := newCaseRun(8*time.Second, 5)
		c.ack()
		c.start(c.plugin("fixture"))
		first := c.register("fixture", transcript{"source": "same-user"})
		AddReportEntry("actual pipe security", inspectPipe())
		secondRoot := filepath.Join(c.root, "second")
		Expect(os.Mkdir(secondRoot, 0o700)).To(Succeed())
		second := exec.Command(harness.daemon, "daemon", "--config", filepath.Join(c.root, "config.json"))
		second.Env = childEnvironment("APH_FIXTURE_ROOT="+secondRoot, "APH_FIXTURE_CONSOLE_DLL="+harness.fixtureDLL)
		var output, diagnostics bytes.Buffer
		second.Stdout = &output
		second.Stderr = &diagnostics
		err := c.runNegative(second, 8*time.Second, 2)
		Expect(err).To(HaveOccurred())
		Expect(second.ProcessState.ExitCode()).To(Equal(1))
		Expect(output.String()).To(BeEmpty())
		Expect(diagnostics.String()).To(ContainSubstring("reserve user control pipe"))
		Expect(readRows(filepath.Join(secondRoot, "versions.jsonl"))).To(HaveLen(1))
		ready, _ := filepath.Glob(filepath.Join(secondRoot, "*", "ready.json"))
		Expect(ready).To(BeEmpty())
		Expect(c.register("fixture", transcript{"source": "same-user"})).To(Equal(first))
		fresh := c.register("fixture", transcript{"source": "after-second"})
		Expect(fresh).NotTo(Equal(first))
		c.action(c.watch(fresh), []string{eventLine(fresh, "first daemon remains usable")}, false)
		results := c.awaitDeliveries(1)
		Expect(results[0]["outcome"]).To(Equal("accepted"))
		c.assertCall(0, fresh, "fixture", "first daemon remains usable")
	})
})
