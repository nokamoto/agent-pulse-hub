package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nokamoto/agent-pulse-hub/internal/domain/jsonvalue"
)

func TestReadFrameCountsLFAndCRLFDelimiters(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{name: "LF", input: "abc\n", limit: 4, want: "abc"},
		{name: "CRLF", input: "abc\r\n", limit: 5, want: "abc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReadFrame(bufio.NewReader(strings.NewReader(test.input)), test.limit)
			if err != nil || string(got) != test.want {
				t.Fatalf("ReadFrame() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := ReadFrame(bufio.NewReader(strings.NewReader("abc\r\n")), 4); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame() error = %v, want frame-too-large", err)
	}
}

func TestReadFrameRequiresDelimiter(t *testing.T) {
	_, err := ReadFrame(bufio.NewReader(strings.NewReader("{}")), 8)
	if !errors.Is(err, ErrPartialFrame) {
		t.Fatalf("ReadFrame() error = %v, want partial-frame", err)
	}
	_, err = ReadFrame(bufio.NewReader(bytes.NewReader(nil)), 8)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("ReadFrame(empty) error = %v, want EOF", err)
	}
}

func TestEncodeFrameEnforcesLimitIncludingLF(t *testing.T) {
	within := jsonvalue.NewString(strings.Repeat("x", MaxFrameBytes-3))
	frame, err := EncodeFrame(within)
	if err != nil || len(frame) != MaxFrameBytes || frame[len(frame)-1] != '\n' {
		t.Fatalf("EncodeFrame() len=%d err=%v", len(frame), err)
	}
	if _, err := EncodeFrame(jsonvalue.NewString(strings.Repeat("x", MaxFrameBytes-2))); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("EncodeFrame() error = %v, want frame-too-large", err)
	}
}

func TestReadFrameExactFullWireBoundsAndMalformedRecovery(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, size := range []int{MaxFrameBytes, MaxFrameBytes + 1} {
			input := strings.Repeat("x", size-len(ending)) + ending
			frame, err := ReadFrame(bufio.NewReader(strings.NewReader(input)), MaxFrameBytes)
			if size == MaxFrameBytes {
				if err != nil || len(frame) != size-len(ending) {
					t.Fatalf("size %d ending %q len %d err %v", size, ending, len(frame), err)
				}
			} else if !errors.Is(err, ErrFrameTooLarge) {
				t.Fatalf("oversize error %v", err)
			}
		}
	}
	reader := bufio.NewReader(bytes.NewReader([]byte{0xff, '\n', '{', '}', '\n'}))
	first, err := ReadFrame(reader, MaxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFrame(first); err == nil {
		t.Fatal("invalid encoding accepted")
	}
	second, err := ReadFrame(reader, MaxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseFrame(second); err != nil {
		t.Fatalf("valid frame after invalid frame: %v", err)
	}
}
