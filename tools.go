//go:build tools

package main

import (
	_ "github.com/magefile/mage/mage"
	_ "golang.org/x/tools/cmd/goimports"
	_ "honnef.co/go/tools/cmd/staticcheck"
	_ "mvdan.cc/gofumpt"
)
