//go:build integration

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Contracts, methods and budgets: docs/design/mvp.md#selected-command-workflows.
var _ = Describe("Manual plugin public Session behavior", Serial, func() {
	It("rejects invalid file content without truncation or retry", Label("case:MVP-V04-MANUAL-INVALID-CONTENT"), SpecTimeout(5*time.Second), func(ctx SpecContext) {
		host := startManualHost(ctx)
		directory := GinkgoT().TempDir()
		path := filepath.Join(directory, "trigger")
		host.watch(ctx, "content", path, true)

		By("emitting all 8192 UTF-8 bytes unchanged and removing the consumed claim")
		valid := strings.Repeat("é", 4096)
		publishManualTrigger(path, []byte(valid))
		host.event(ctx, "content", valid)
		Expect(manualClaims(directory)).To(BeEmpty())
		_, err := os.Lstat(path)
		Expect(os.IsNotExist(err)).To(BeTrue())

		By("rejecting oversized and invalid UTF-8 files without event, truncation or claimed-file retry")
		invalid := [][]byte{bytes.Repeat([]byte("x"), 8193), {'x', 0xff}}
		for index, content := range invalid {
			publishManualTrigger(path, content)
			host.waitRejections(ctx, index+1)
			host.noFrames(ctx, 250*time.Millisecond)
			_, err := os.Lstat(path)
			Expect(os.IsNotExist(err)).To(BeTrue())
			claims := manualClaims(directory)
			Expect(claims).To(HaveLen(index + 1))
			for _, claim := range claims {
				Expect(host.diagnostics.String()).To(ContainSubstring(filepath.Base(claim)))
			}
		}

		By("continuing to emit a valid marker exactly once on the original watch")
		marker := "PULSE-MANUAL-VALID\n日本語"
		publishManualTrigger(path, []byte(marker))
		host.event(ctx, "content", marker)
		host.noFrames(ctx, 250*time.Millisecond)
		Expect(strings.Count(host.diagnostics.String(), "manual_plugin_trigger_rejected")).To(Equal(2))
		claims := manualClaims(directory)
		Expect(claims).To(HaveLen(2))
		retained := make([][]byte, 0, 2)
		for _, claim := range claims {
			content, err := os.ReadFile(claim)
			Expect(err).NotTo(HaveOccurred())
			retained = append(retained, content)
		}
		Expect(retained).To(ConsistOf(invalid[0], invalid[1]))
		_, err = os.Lstat(path)
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
	It("rejects a duplicate cleaned path and an existing trigger", Label("case:MVP-V02-MANUAL-PATHS"), SpecTimeout(5*time.Second), func(ctx SpecContext) {
		host := startManualHost(ctx)
		directory := GinkgoT().TempDir()
		path := filepath.Join(directory, "trigger")
		By("watching an absent absolute path and rejecting another spelling cleaned to that exact path")
		host.watch(ctx, "original", path, true)
		unclean := directory + string(filepath.Separator) + "unused" + string(filepath.Separator) + ".." + string(filepath.Separator) + "trigger"
		Expect(filepath.Clean(unclean)).To(Equal(path))
		host.watch(ctx, "duplicate", unclean, false)

		By("rejecting an existing file without modifying its contents")
		existing := filepath.Join(directory, "existing")
		Expect(os.WriteFile(existing, []byte("existing marker"), 0o600)).To(Succeed())
		host.watch(ctx, "existing", existing, false)
		content, err := os.ReadFile(existing)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal("existing marker"))

		By("accepting a distinct absent path and consuming exactly one marker on its subscription")
		distinct := filepath.Join(directory, "distinct")
		host.watch(ctx, "distinct", distinct, true)
		publishManualTrigger(distinct, []byte("PULSE-MANUAL-DISTINCT"))
		host.event(ctx, "distinct", "PULSE-MANUAL-DISTINCT")
		host.noFrames(ctx, 250*time.Millisecond)
		_, err = os.Lstat(distinct)
		Expect(os.IsNotExist(err)).To(BeTrue())
		Expect(manualClaims(directory)).To(BeEmpty())
	})
})
