//go:build integration

package layout_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIntegrationLayout(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Plugin layout tooling fixture")
}

func TestUnitMustNotRun(t *testing.T) {
	t.Fatal("ordinary unit tests must not run in integration targets")
}

var _ = Describe("layout qualification", func() {
	It("passes", Label("case:LAYOUT-PASS"), func() {
		Expect(true).To(BeTrue())
	})
	It("remains pending outside the acceptance scope", Label("case:LAYOUT-PENDING"), Pending, func() {})
})
