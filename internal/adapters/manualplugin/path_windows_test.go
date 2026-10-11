//go:build windows

package manualplugin

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
	"go.uber.org/mock/gomock"
)

func TestNormalizeTriggerPathCanonicalizesLocalComponents(t *testing.T) {
	path, parent, name, err := normalizeTriggerPath(`c:/events/../tmp/./next.txt`)
	if err != nil || path != `C:\tmp\next.txt` || parent != `C:\tmp` || name != "next.txt" {
		t.Fatalf("normalized = %q, %q, %q, %v", path, parent, name, err)
	}
}

func TestNormalizeTriggerPathRejectsUnsupportedForms(t *testing.T) {
	for _, input := range []string{`\\server\share\next.txt`, `\\?\C:\next.txt`, `\\.\C:\next.txt`, `C:\..\next.txt`, `C:\tmp\next.txt:stream`, `C:\tmp\`, `C:\tmp\name.`, `C:\tmp\name `, `C:\tmp.\name`, `next.txt`, `C:next.txt`, `C:\.`} {
		if _, _, _, err := normalizeTriggerPath(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestWatchTargetUsesDirectoryIdentityAndRejectsLookupFailures(t *testing.T) {
	for _, scenario := range []string{"valid", "remote drive", "directory lookup", "existing", "lookup denied"} {
		t.Run(scenario, func(t *testing.T) {
			files := NewMocktriggerFiles(gomock.NewController(t))
			files.EXPECT().LocalDrive(`C:\`).Return(scenario != "remote drive")
			if scenario != "remote drive" {
				var lookupErr error
				if scenario == "directory lookup" {
					lookupErr = errors.New("identity unavailable")
				}
				files.EXPECT().DirectoryKey(`C:\alias`, "next.txt").Return(targetKey{volume: 4, index: 9, name: "next.txt"}, lookupErr)
				if lookupErr == nil {
					existsErr := error(os.ErrNotExist)
					if scenario == "existing" {
						existsErr = nil
					}
					if scenario == "lookup denied" {
						existsErr = os.ErrPermission
					}
					files.EXPECT().Exists(`C:\alias\next.txt`).Return(existsErr)
				}
			}
			target, err := parseWatchTargetWithFiles(`C:/alias/./next.txt`, files)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("target = %+v, err = %v", target, err)
			}
			if err == nil && (target.key.volume != 4 || target.key.index != 9 || target.key.name != "next.txt") {
				t.Fatalf("lost parent identity: %+v", target)
			}
		})
	}
}

func TestManualWatchTargetComparisonUsesParentIdentityAndOrdinalName(t *testing.T) {
	first := targetKey{volume: 1, index: 2, name: "next.txt"}
	for _, test := range []struct {
		key   targetKey
		equal bool
	}{
		{targetKey{1, 2, "NEXT.TXT"}, true}, {targetKey{2, 2, "next.txt"}, false}, {targetKey{1, 3, "next.txt"}, false}, {targetKey{1, 2, "other.txt"}, false}, {targetKey{1, 2, "next\x00.txt"}, false},
	} {
		if sameTarget(first, test.key) != test.equal {
			t.Errorf("sameTarget(%+v, %+v) != %v", first, test.key, test.equal)
		}
	}
}

func TestWatchArgumentsRejectInvalidSchemaBeforeFileLookup(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{}`, `{"trigger_file":1}`, `{"trigger_file":"C:\\next.txt","extra":true}`, `{"trigger_file":"relative.txt"}`} {
		value, err := jsonvalue.Parse([]byte(input), 65536)
		if err != nil {
			t.Fatal(err)
		}
		files := NewMocktriggerFiles(gomock.NewController(t))
		if _, err := watchTargetFromArgumentsWithFiles(value, files); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

type faultReadCloser struct {
	io.Reader
	closeErr error
}

func (reader faultReadCloser) Close() error { return reader.closeErr }

type failingReader struct{ err error }

func (reader failingReader) Read([]byte) (int, error) { return 0, reader.err }

func TestConsumeTriggerPreservesBoundAndRejectsFileFaultsWithoutRemovalOrRetry(t *testing.T) {
	valid := strings.Repeat("é", 4096)
	for _, test := range []struct {
		name, input string
		fault       string
		valid       bool
	}{
		{"valid bound", valid, "", true},
		{"empty", "", "", false},
		{"oversized", valid + "a", "", false},
		{"invalid UTF-8", "\xff", "", false},
		{"claim", "", "claim", false},
		{"open", "", "open", false},
		{"read", "", "read", false},
		{"close", "ok", "close", false},
		{"remove", "ok", "remove", false},
		{"absent", "", "absent", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := NewMocktriggerFiles(gomock.NewController(t))
			target := watchTarget{path: `C:\events\next.txt`}
			const claimed = `C:\events\.agent-pulse-hub-claim-test`
			fault := errors.New(test.fault)
			if test.fault == "claim" || test.fault == "absent" {
				claimErr := fault
				if test.fault == "absent" {
					claimErr = os.ErrNotExist
				}
				files.EXPECT().Claim(target).Return("", claimErr)
			} else {
				files.EXPECT().Claim(target).Return(claimed, nil)
				if test.fault == "open" {
					files.EXPECT().Open(claimed).Return(nil, fault)
				} else {
					var reader io.Reader = bytes.NewBufferString(test.input)
					if test.fault == "read" {
						reader = failingReader{fault}
					}
					var closeErr error
					if test.fault == "close" {
						closeErr = fault
					}
					files.EXPECT().Open(claimed).Return(faultReadCloser{reader, closeErr}, nil)
					if test.valid {
						files.EXPECT().Remove(claimed).Return(nil)
					}
					if test.fault == "remove" {
						files.EXPECT().Remove(claimed).Return(fault)
					}
				}
			}
			contextText, claimPath, err := consumeTriggerWithFiles(target, files)
			if test.valid {
				if err != nil || contextText != test.input || claimPath != claimed {
					t.Fatalf("got %q, %q, %v", contextText, claimPath, err)
				}
				return
			}
			if contextText != "" {
				t.Fatal("failure emitted context")
			}
			if test.fault == "absent" {
				if err != nil || claimPath != "" {
					t.Fatalf("absent = %q, %v", claimPath, err)
				}
				return
			}
			if err == nil {
				t.Fatal("fault accepted")
			}
			wantPath := claimed
			if test.fault == "claim" {
				wantPath = target.path
			}
			if claimPath != wantPath {
				t.Fatalf("diagnostic path %q, want %q", claimPath, wantPath)
			}
		})
	}
}
