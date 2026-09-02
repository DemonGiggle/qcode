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

func TestInterruptReaderRoutesPageKeys(t *testing.T) {
	reader := newInterruptReader(nil)
	var directions []int
	reader.setPageHandler(func(direction int) {
		directions = append(directions, direction)
	})

	reader.route([]byte(pageUpSequence + pageDownSequence))
	if len(directions) != 2 || directions[0] != 1 || directions[1] != -1 {
		t.Fatalf("page directions = %v", directions)
	}
	select {
	case key := <-reader.data:
		t.Fatalf("page key leaked to line editor as %q", key)
	default:
	}
}

func TestInterruptReaderRecognizesSplitPageSequence(t *testing.T) {
	reader := newInterruptReader(nil)
	called := 0
	reader.setPageHandler(func(direction int) {
		if direction != 1 {
			t.Fatalf("direction = %d", direction)
		}
		called++
	})

	reader.route([]byte("\x1b["))
	reader.route([]byte("5~"))
	if called != 1 {
		t.Fatalf("page handler called %d times", called)
	}
}

func TestInterruptReaderForwardsOtherEscapeSequences(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.route([]byte("\x1b[A"))
	buffer := make([]byte, 3)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "\x1b[A" {
		t.Fatalf("input = %q", buffer)
	}
}

func TestInterruptReaderRawModeForwardsControlAndPageKeys(t *testing.T) {
	reader := newInterruptReader(nil)
	called := false
	reader.setPageHandler(func(int) { called = true })
	reader.setRaw(true)
	reader.route([]byte{ctrlC})
	reader.route([]byte(pageUpSequence))

	buffer := make([]byte, 1+len(pageUpSequence))
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if buffer[0] != ctrlC || string(buffer[1:]) != pageUpSequence || called {
		t.Fatalf("raw input = %q, page called = %v", buffer, called)
	}
}
