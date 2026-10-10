//go:build mage

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
)

func TestDecodeInventory(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		valid       bool
	}{
		{"valid", `{"suites":[{"package":"./cmd/daemon","cases":["V01","V02"]}]}`, true},
		{"missing suites", `{}`, false},
		{"empty suites", `{"suites":[]}`, false},
		{"empty cases", `{"suites":[{"package":"./cmd/daemon","cases":[]}]}`, false},
		{"empty ID", `{"suites":[{"package":"./cmd/daemon","cases":[""]}]}`, false},
		{"duplicate ID", `{"suites":[{"package":"./cmd/daemon","cases":["V01","V01"]}]}`, false},
		{"cross suite duplicate", `{"suites":[{"package":"./cmd/daemon","cases":["V01"]},{"package":"./cmd/plugin","cases":["V01"]}]}`, false},
		{"duplicate suite", `{"suites":[{"package":"./cmd/daemon","cases":["V01"]},{"package":"./cmd/daemon","cases":["V02"]}]}`, false},
		{"filter injection", `{"suites":[{"package":"./cmd/daemon","cases":["V01 || other"]}]}`, false},
		{"wildcard package", `{"suites":[{"package":"./cmd/...","cases":["V01"]}]}`, false},
		{"outside repository", `{"suites":[{"package":"./../other","cases":["V01"]}]}`, false},
		{"absolute package", `{"suites":[{"package":"/cmd/daemon","cases":["V01"]}]}`, false},
		{"unknown field", `{"suites":[],"typo":true}`, false},
		{"trailing data", `{"suites":[]} {}`, false},
		{"invalid JSON", `{`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeInventory(strings.NewReader(tt.input))
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}

func TestCheckAcceptance(t *testing.T) {
	root := filepath.Join("repository", "root")
	inventory := acceptanceInventory{Suites: []acceptanceSuite{{Package: "./cmd/daemon", Cases: []string{"V01", "V02"}}}}
	passed := func(id string) types.SpecReport {
		return types.SpecReport{LeafNodeType: types.NodeTypeIt, LeafNodeLabels: []string{"case:" + id}, State: types.SpecStatePassed}
	}
	for _, tt := range []struct {
		name  string
		edit  func(*types.Report)
		valid bool
	}{
		{"all passed", func(*types.Report) {}, true},
		{"pending", func(r *types.Report) { r.SpecReports[1].State = types.SpecStatePending }, false},
		{"runtime skip or filtered", func(r *types.Report) { r.SpecReports[1].State = types.SpecStateSkipped }, false},
		{"failed", func(r *types.Report) { r.SpecReports[1].State = types.SpecStateFailed }, false},
		{"panicked", func(r *types.Report) { r.SpecReports[1].State = types.SpecStatePanicked }, false},
		{"interrupted", func(r *types.Report) { r.SpecReports[1].State = types.SpecStateInterrupted }, false},
		{"timeout", func(r *types.Report) { r.SpecReports[1].State = types.SpecStateTimedout }, false},
		{"missing or deleted", func(r *types.Report) { r.SpecReports = r.SpecReports[:1] }, false},
		{"duplicate", func(r *types.Report) { r.SpecReports = append(r.SpecReports, passed("V01")) }, false},
		{"shared spec IDs", func(r *types.Report) {
			r.SpecReports[0].LeafNodeLabels = []string{"case:V01", "case:V02"}
			r.SpecReports = r.SpecReports[:1]
		}, false},
		{"suite failed", func(r *types.Report) { r.SuiteSucceeded = false }, false},
		{"focused", func(r *types.Report) { r.SuiteHasProgrammaticFocus = true }, false},
		{"dry run", func(r *types.Report) { r.SuiteConfig.DryRun = true }, false},
		{"wrong suite", func(r *types.Report) { r.SuitePath = filepath.Join(root, "cmd", "other") }, false},
		{"hook cannot replace case", func(r *types.Report) { r.SpecReports[1].LeafNodeType = types.NodeTypeBeforeSuite }, false},
		{"future scope pending", func(r *types.Report) {
			spec := passed("FUTURE")
			spec.State = types.SpecStatePending
			r.SpecReports = append(r.SpecReports, spec)
		}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			report := types.Report{SuitePath: filepath.Join(root, "cmd", "daemon"), SuiteSucceeded: true, SpecReports: types.SpecReports{passed("V01"), passed("V02")}}
			tt.edit(&report)
			err := checkAcceptance(root, inventory, []types.Report{report})
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
	if err := checkAcceptance(root, inventory, nil); err == nil {
		t.Fatal("empty execution report passed")
	}
	if err := checkAcceptance(root, acceptanceInventory{}, nil); err == nil {
		t.Fatal("empty inventory passed")
	}
	report := types.Report{SuitePath: filepath.Join(root, "cmd", "daemon"), SuiteSucceeded: true}
	if err := checkAcceptance(root, inventory, []types.Report{report, report}); err == nil {
		t.Fatal("duplicate suite reports passed")
	}
}

func TestIntegrationArgs(t *testing.T) {
	inventory := acceptanceInventory{Suites: []acceptanceSuite{{Package: "./cmd/daemon", Cases: []string{"V01"}}, {Package: "./cmd/plugin", Cases: []string{"V02"}}}}
	args := strings.Join(integrationArgs(inventory, "reports"), " ")
	for _, want := range []string{"--tags=integration", "--timeout=2m", "--json-report=report.json", "--label-filter=case:V01 || case:V02", "./cmd/daemon ./cmd/plugin -- -test.run=^TestIntegration"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
}
