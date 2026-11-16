package tui

import (
	"bytes"
	"testing"
)

func TestRawLFWriterConvertsBareLFWithoutDoublingCRLF(t *testing.T) {
	var out bytes.Buffer
	w := &rawLFWriter{out: &out}

	if _, err := w.Write([]byte("line1\nline2\r\nline3\r")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := w.Write([]byte("\nline4\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	want := "line1\r\nline2\r\nline3\r\nline4\r\n"
	if got := out.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
