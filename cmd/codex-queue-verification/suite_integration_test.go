//go:build integration

package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegrationCodexQueue(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Codex Queue standalone action and registration acceptance")
}
