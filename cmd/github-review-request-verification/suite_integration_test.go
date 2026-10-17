//go:build integration

package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegrationGitHubReviewRequest(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GitHub review request public plugin acceptance")
}
