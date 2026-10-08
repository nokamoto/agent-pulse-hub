//go:build tools

package main

import (
	_ "github.com/magefile/mage/mage"
	_ "golang.org/x/tools/cmd/goimports"
	_ "mvdan.cc/gofumpt"
)
