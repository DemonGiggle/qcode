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

func TestInterruptReaderRoutesTabKeysDuringTask(t *testing.T) {
	reader := newInterruptReader(nil)
	var directions []int
	reader.setTabHandler(func(direction int) { directions = append(directions, direction) })
	cancelled := false
	reader.setCancel(func() { cancelled = true })

	reader.route([]byte(ctrlPageUpSequence + ctrlPageDownSequence + rxvtCtrlPageUp + rxvtCtrlPageDown + altPreviousTab + altNextTab + "discarded"))
	want := []int{-1, 1, -1, 1, -1, 1}
	if len(directions) != len(want) {
		t.Fatalf("tab directions = %v", directions)
	}
	for index := range want {
		if directions[index] != want[index] {
			t.Fatalf("tab directions = %v", directions)
		}
	}
	if cancelled {
		t.Fatal("tab navigation cancelled the task")
	}
	select {
	case key := <-reader.data:
		t.Fatalf("task input leaked to line editor as %q", key)
	default:
	}
}

func TestInterruptReaderRecognizesSplitTabSequences(t *testing.T) {
	for _, sequence := range tabKeySequences {
		t.Run(sequence.value, func(t *testing.T) {
			reader := newInterruptReader(nil)
			var direction int
			reader.setTabHandler(func(value int) { direction = value })

			for index := range sequence.value {
				reader.route([]byte(sequence.value[index : index+1]))
			}
			if direction != sequence.direction {
				t.Fatalf("direction = %d, want %d", direction, sequence.direction)
			}
		})
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
	reader.setTabHandler(func(int) { called = true })
	reader.setRaw(true)
	reader.route([]byte{ctrlC})
	reader.route([]byte(pageUpSequence))
	reader.route([]byte(altNextTab))

	buffer := make([]byte, 1+len(pageUpSequence)+len(altNextTab))
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if buffer[0] != ctrlC || string(buffer[1:]) != pageUpSequence+altNextTab || called {
		t.Fatalf("raw input = %q, page called = %v", buffer, called)
	}
}
