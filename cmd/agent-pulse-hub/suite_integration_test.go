//go:build integration

package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegrationMVP(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent Pulse Hub MVP command acceptance")
}
