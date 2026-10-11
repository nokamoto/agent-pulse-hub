//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Contracts, methods and budgets: docs/design/mvp.md#selected-command-workflows.
var _ = Describe("Manual plugin public Session behavior", Serial, func() {
	It("rejects invalid file content without truncation or retry", Pending, Label("case:MVP-V04-MANUAL-INVALID-CONTENT"), func() {
		By("hosting the real plugin with go-plugin and publishing 8192 valid bytes, 8193 bytes, invalid UTF-8, then a valid marker")
		By("observing unchanged valid contexts only, invalid-file diagnostics and continued healthy emission")
	})
	It("rejects a duplicate cleaned path and an existing trigger", Pending, Label("case:MVP-V02-MANUAL-PATHS"), func() {
		By("watching an absent absolute path and rejecting another spelling cleaned to that exact path")
		By("rejecting an existing file, accepting a distinct absent path and consuming one published marker")
	})
})
