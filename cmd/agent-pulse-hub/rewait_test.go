package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRetiredRewaitCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"rewait", "--subscription-id", "s1", "--operation-id", "1700000000000:0123456789abcdef0123456789abcdef", "--confirmation-position-file", "position.json"}, &stdout, &stderr); code == 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("retired rewait command returned %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}
