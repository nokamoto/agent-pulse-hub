//go:build integration

package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegrationGitHubPRWatch(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GitHub PR watch public plugin and repeated waiting acceptance")
}
