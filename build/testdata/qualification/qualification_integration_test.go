//go:build integration

package qualification_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegrationQualification(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Integration tooling qualification")
}

func TestUnitMustNotRun(t *testing.T) {
	t.Fatal("the integration runner executed an ordinary unit test")
}

var _ = Describe("report gate fixtures", func() {
	It("passes", Label("case:TOOL-PASS"), func() {
		Expect(1 + 1).To(Equal(2))
	})
	It("is pending", Label("case:TOOL-PENDING"), Pending, func() {})
	It("skips at runtime", Label("case:TOOL-SKIP"), func() {
		Skip("intentional qualification fixture")
	})
	It("fails", Label("case:TOOL-FAIL"), func() {
		Fail("intentional qualification fixture")
	})
})
