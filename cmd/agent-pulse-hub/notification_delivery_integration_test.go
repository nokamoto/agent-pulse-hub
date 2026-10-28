//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/notification-delivery.md. These executable cases
// retain the real daemon, public clients, queue, scheduler, lifecycle and store.
// Command-owned model peers replace plugin endpoints only. Reuse mvpEnv builds,
// normal stop/restart, temporary trees, numbered commands and observable gates.
// Policy/clock/storage-error/input matrices belong to implementation unit tests;
// these workflows select the assembled boundaries those tests cannot establish.
var _ = Describe("Notification delivery acceptance", Serial, func() {
	It("exposes correlated status and retries only proven unsubmitted attempts", Label("case:ND-V01-STATUS-RETRY"), func() {
		runNDStatus()
	})

	It("retains evidence and resumes only eligible remaining attempts after normal restart", Label("case:ND-V02-RESTART"), func() {
		runNDRestart()
	})

	It("fences scheduled work on unsubscribe while retaining issued-attempt evidence", Label("case:ND-V03-CANCEL"), func() {
		runNDCancel()
	})
})
