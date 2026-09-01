package tui

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestInterruptReaderCancelsActiveTask(t *testing.T) {
	input, writer := io.Pipe()
	reader := newInterruptReader(input)
	reader.start()
	ctx, cancel := context.WithCancel(context.Background())
	reader.setCancel(cancel)

	if _, err := writer.Write([]byte{ctrlC}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Ctrl+C did not cancel active task")
	}
	_ = writer.Close()
}

func TestInterruptReaderKeepsApplicationOpenWhenIdle(t *testing.T) {
	input, writer := io.Pipe()
	reader := newInterruptReader(input)
	reader.start()

	if _, err := writer.Write([]byte{ctrlC}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if buffer[0] != ctrlU || buffer[1] != '\r' {
		t.Fatalf("idle Ctrl+C routed as %v", buffer)
	}
	_ = writer.Close()
}

func TestInterruptReaderDiscardsOtherInputDuringTask(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.setCancel(func() {})
	reader.route([]byte("ignored"))
	reader.setCancel(nil)
	reader.route([]byte("kept"))
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "kept" {
		t.Fatalf("input = %q", buffer)
	}
}
