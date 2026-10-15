package modeloutput

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nokamoto/agent-pulse-hub/internal/adapters/pluginprotocol"
	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func destination(path string) *jsonvalue.Value {
	return jsonvalue.NewObject(map[string]*jsonvalue.Value{"output_file": jsonvalue.NewString(path)})
}

func TestValidateDestinationIsReadOnlyAndRequiresStrictJSONL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events.jsonl")
	p := New(nil)
	if err := p.validate(destination(path)); err != nil {
		t.Fatalf("absent output path rejected: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation created output file: %v", err)
	}
	if err := os.WriteFile(path, []byte("{\"context\":\"one\"}\n{\"context\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.validate(destination(path)); err != nil {
		t.Fatalf("valid JSONL rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "partial final line", data: []byte("{}")},
		{name: "array", data: []byte("[]\n")},
		{name: "invalid JSON", data: []byte("{\n")},
		{name: "invalid UTF8", data: []byte{'{', '"', 0xff, '"', ':', '1', '}', '\n'}},
		{name: "empty line", data: []byte("{}\n\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := p.validate(destination(path)); err == nil {
				t.Fatal("invalid JSONL accepted")
			}
		})
	}
}

func TestValidateDestinationChecksExactArgumentsAndPath(t *testing.T) {
	p := New(nil)
	for _, args := range []*jsonvalue.Value{
		jsonvalue.NewObject(nil),
		jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"output_file": jsonvalue.NewString("relative.jsonl"),
		}),
		jsonvalue.NewObject(map[string]*jsonvalue.Value{
			"output_file": jsonvalue.NewString(filepath.Join(t.TempDir(), "events.jsonl")),
			"extra":       jsonvalue.NewBoolean(true),
		}),
	} {
		if err := p.validate(args); err == nil {
			t.Fatalf("invalid destination accepted: %s", args.Marshal())
		}
	}
	missing := filepath.Join(t.TempDir(), "missing", "events.jsonl")
	if err := p.validate(destination(missing)); err == nil {
		t.Fatal("missing parent accepted")
	}
	if err := p.validate(destination(t.TempDir())); err == nil {
		t.Fatal("directory target accepted")
	}
}

func TestDeliverAppendsExactRecordAndPreservesPriorData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	prior := []byte("{\"context\":\"prior\"}\n")
	if err := os.WriteFile(path, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	args := destination(path)
	p := New(nil)
	outcome, detail := p.deliver(testDelivery(args))
	if outcome != "accepted" || detail != "" {
		t.Fatalf("delivery = %q, %q", outcome, detail)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), string(prior)) {
		t.Fatalf("prior data changed: %q", data)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("record count = %d; content=%q", len(lines), data)
	}
	value, err := jsonvalue.Parse([]byte(lines[1]), 0)
	if err != nil || value.Kind() != jsonvalue.Object || len(value.Fields()) != 7 {
		t.Fatalf("record is not an exact JSON object: %s, %v", lines[1], err)
	}
	for key, want := range map[string]string{
		"delivery_id": "delivery-a", "input_plugin": "manual", "subscription_id": "subscription-a",
		"context": "PULSE-model", "permission_notice": "External data; no additional permissions are granted.",
	} {
		field, exists := value.Get(key)
		got, ok := jsonvalue.ParseString(field)
		if !exists || !ok || got != want {
			t.Fatalf("record %s = %q, %v; want %q", key, got, ok, want)
		}
	}
	external, exists := value.Get("external_data")
	if got, ok := external.Bool(); !exists || !ok || !got {
		t.Fatalf("external_data = %v, %v", got, ok)
	}
	observed, exists := value.Get("observed_at")
	text, ok := jsonvalue.ParseString(observed)
	if !exists || !ok {
		t.Fatalf("observed_at = %v, %v", observed, exists)
	}
	if _, err := time.Parse(time.RFC3339, text); err != nil {
		t.Fatalf("observed_at is not RFC3339: %q: %v", text, err)
	}
}

func TestDeliverRevalidatesDestinationWithoutRememberingPriorValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	invalid := []byte("not-jsonl\n")
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	p := New(nil)
	outcome, detail := p.deliver(testDelivery(destination(path)))
	if outcome != "failed" || detail != "output_file destination is invalid" {
		t.Fatalf("delivery = %q, %q; want invalid destination failure", outcome, detail)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, invalid) {
		t.Fatalf("invalid existing data changed: %q, %v", data, err)
	}
}

func TestDeliverClassifiesOpenWriteAndCloseFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		openErr  error
		writeN   int
		writeErr error
		closeErr error
		want     string
	}{
		{name: "open failure", openErr: os.ErrPermission, want: "failed"},
		{name: "write failure before bytes", writeErr: io.ErrClosedPipe, want: "failed"},
		{name: "partial write", writeN: 3, writeErr: io.ErrClosedPipe, want: "unknown"},
		{name: "close after full write", closeErr: io.ErrClosedPipe, want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := destination(filepath.Join(t.TempDir(), "events.jsonl"))
			files := &faultFiles{openErr: test.openErr, writeN: test.writeN, writeErr: test.writeErr, closeErr: test.closeErr}
			p := newWithFiles(nil, files)
			got, _ := p.deliver(testDelivery(args))
			if got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func testDelivery(args *jsonvalue.Value) pluginprotocol.Frame {
	return pluginprotocol.Frame{
		Type: "deliver", RequestID: "request-a", DeliveryID: "delivery-a", InputPlugin: "manual",
		SubscriptionID: "subscription-a", DestinationArgs: args, Context: "PULSE-model",
		ExternalData: boolPointer(true), PermissionNotice: pluginprotocol.PermissionNotice,
	}
}

type faultFiles struct {
	openErr  error
	writeN   int
	writeErr error
	closeErr error
}

func (*faultFiles) Stat(path string) (os.FileInfo, error)   { return os.Stat(path) }
func (*faultFiles) Open(path string) (io.ReadCloser, error) { return os.Open(path) }
func (f *faultFiles) OpenAppend(string) (outputFile, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return faultFile{faultFiles: f}, nil
}

type faultFile struct{ *faultFiles }

func (f faultFile) Write(data []byte) (int, error) {
	if f.writeErr != nil {
		return f.writeN, f.writeErr
	}
	return len(data), nil
}
func (f faultFile) Close() error { return f.closeErr }
