//go:build tools

package main

import (
	_ "github.com/magefile/mage/mage"
	_ "github.com/onsi/ginkgo/v2"
	_ "github.com/onsi/ginkgo/v2/ginkgo"

	// Keep fixture-only Gomega imports in the module graph during tidy.
	_ "github.com/onsi/gomega"
	_ "go.uber.org/mock/mockgen"
	_ "golang.org/x/tools/cmd/goimports"
	_ "honnef.co/go/tools/cmd/staticcheck"
	_ "mvdan.cc/gofumpt"
)
