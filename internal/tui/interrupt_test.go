package tui

import (
	"context"
	"fmt"
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

func TestInterruptReaderKeepsOtherInputDuringTask(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.setCancel(func() {})
	reader.route([]byte("queued"))
	reader.setCancel(nil)
	reader.route([]byte("kept"))
	buffer := make([]byte, len("queuedkept"))
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "queuedkept" {
		t.Fatalf("input = %q", buffer)
	}
}

func TestInterruptReaderDefersInjectedInputWhileRawSelectorIsActive(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.setRaw(true)
	reader.inject([]byte("remote prompt\r"))
	reader.data <- 'k'

	buffer := make([]byte, 1)
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "k" {
		t.Fatalf("raw selector input = %q, %v", buffer, err)
	}
	reader.wakeRaw()
	if _, err := io.ReadFull(reader, buffer); err != nil || buffer[0] != 0 {
		t.Fatalf("raw selector wake = %q, %v", buffer, err)
	}

	reader.setRaw(false)
	buffer = make([]byte, len("remote prompt\r"))
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "remote prompt\r" {
		t.Fatalf("deferred injected input = %q, %v", buffer, err)
	}
}

func TestInterruptReaderRawInputBackpressureDoesNotBlockReads(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.setRaw(true)
	routed := make(chan struct{})
	go func() {
		reader.route(make([]byte, cap(reader.data)+1))
		close(routed)
	}()
	// Wait until the producer fills the buffer and must wait for a reader.
	deadline := time.Now().Add(time.Second)
	for len(reader.data) < cap(reader.data) {
		if time.Now().After(deadline) {
			t.Fatal("raw input did not fill the buffer")
		}
		time.Sleep(time.Millisecond)
	}
	read := make(chan error, 1)
	go func() {
		var key [1]byte
		_, err := reader.Read(key[:])
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		// Release the blocked producer directly so the failed test leaves no
		// goroutines behind, even if Read is waiting for the producer's mutex.
		<-reader.data
		<-routed
		<-read
		t.Fatal("raw input holds the reader mutex while waiting for buffer space")
	}
	select {
	case <-routed:
	case <-time.After(time.Second):
		t.Fatal("reading did not release raw input backpressure")
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

func TestInterruptReaderRoutesHistoryBoundariesDuringTask(t *testing.T) {
	for _, sequence := range historyBoundarySequences {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/split=%v", sequence.value, split), func(t *testing.T) {
				reader := newInterruptReader(nil)
				calls := 0
				reader.setHistoryBoundaryHandler(func(beginning bool) bool {
					calls++
					if beginning != sequence.beginning {
						t.Fatalf("beginning = %v, want %v", beginning, sequence.beginning)
					}
					return true
				})
				cancelled := false
				reader.setCancel(func() { cancelled = true })
				input := []byte(sequence.value + "draft\x01\x05")
				if split {
					for _, key := range input {
						reader.route([]byte{key})
					}
				} else {
					reader.route(input)
				}
				if calls != 1 || cancelled {
					t.Fatalf("boundary calls = %d, cancelled = %v", calls, cancelled)
				}
				buffer := make([]byte, len("draft\x01\x05"))
				if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "draft\x01\x05" {
					t.Fatalf("editor input = %q, err = %v", buffer, err)
				}
				select {
				case key := <-reader.data:
					t.Fatalf("boundary key leaked to editor as %q", key)
				default:
				}
				reader.route([]byte{ctrlC})
				if !cancelled {
					t.Fatal("history navigation prevented cancellation")
				}
			})
		}
	}
}

func TestInterruptReaderForwardsHistoryBoundariesInRawMode(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.setHistoryBoundaryHandler(func(bool) bool { t.Fatal("raw key navigated transcript"); return true })
	reader.setRaw(true)
	for _, sequence := range historyBoundarySequences {
		reader.route([]byte(sequence.value))
		buffer := make([]byte, len(sequence.value))
		if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != sequence.value {
			t.Fatalf("raw input = %q, want %q, err = %v", buffer, sequence.value, err)
		}
	}
}

func TestInterruptReaderTogglesQueuePanelWithoutEatingTyping(t *testing.T) {
	reader := newInterruptReader(nil)
	called := 0
	reader.setQueueHandler(func() { called++ })
	for _, key := range []byte(altQueuePanel + "draft") {
		reader.route([]byte{key})
	}
	if called != 1 {
		t.Fatalf("queue toggled %d times", called)
	}
	buffer := make([]byte, len("draft"))
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "draft" {
		t.Fatalf("editor input = %q, %v", buffer, err)
	}
	reader.setRaw(true)
	reader.route([]byte(altQueuePanel))
	buffer = make([]byte, len(altQueuePanel))
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != altQueuePanel || called != 1 {
		t.Fatalf("raw input = %q, toggles = %d, err = %v", buffer, called, err)
	}
}

func TestInterruptReaderRoutesTabKeysAndTypingDuringTask(t *testing.T) {
	reader := newInterruptReader(nil)
	var directions []int
	reader.setTabHandler(func(direction int) { directions = append(directions, direction) })
	cancelled := false
	reader.setCancel(func() { cancelled = true })

	reader.route([]byte(ctrlPageUpSequence + ctrlPageDownSequence + rxvtCtrlPageUp + rxvtCtrlPageDown + altPreviousTab + altNextTab + "queued"))
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
	buffer := make([]byte, len("queued"))
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "queued" {
		t.Fatalf("queued input = %q, %v", buffer, err)
	}
}

func TestInterruptReaderPagesDuringTaskAndKeepsTyping(t *testing.T) {
	reader := newInterruptReader(nil)
	var directions []int
	reader.setPageHandler(func(d int) { directions = append(directions, d) })
	cancelled := false
	reader.setCancel(func() { cancelled = true })
	for _, key := range []byte(pageUpSequence + pageDownSequence + "queued") {
		reader.route([]byte{key})
	}
	if len(directions) != 2 || directions[0] != 1 || directions[1] != -1 || cancelled {
		t.Fatalf("directions=%v cancelled=%v", directions, cancelled)
	}
	buffer := make([]byte, len("queued"))
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "queued" {
		t.Fatalf("queued input = %q, %v", buffer, err)
	}
	reader.route([]byte{ctrlC})
	if !cancelled {
		t.Fatal("paging prevented cancellation")
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

func TestSelectorKeyRecognizesStandaloneEscapeFromInterruptReader(t *testing.T) {
	reader := newInterruptReader(nil)
	reader.setRaw(true)
	reader.route([]byte{27})
	started := time.Now()

	key, err := readSelectorKey(reader)
	if err != nil || key != "\x1b" {
		t.Fatalf("key = %q, err = %v", key, err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("standalone Escape took %s", elapsed)
	}
}

func TestSelectorKeyKeepsTerminalEscapeSequencesTogether(t *testing.T) {
	for _, want := range []string{arrowDownSequence, selectorPageDown} {
		reader := newInterruptReader(nil)
		reader.setRaw(true)
		reader.route([]byte(want))
		key, err := readSelectorKey(reader)
		if err != nil || key != want {
			t.Fatalf("key = %q, want %q, err = %v", key, want, err)
		}
	}
}

func TestInterruptReaderForwardsHistoryBoundariesWhileEditing(t *testing.T) {
	for _, sequence := range historyBoundarySequences {
		for _, split := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/split=%v", sequence.value, split), func(t *testing.T) {
				reader := newInterruptReader(nil)
				calls := 0
				reader.setHistoryBoundaryHandler(func(bool) bool { calls++; return false })
				input := []byte(sequence.value + "draft")
				if split {
					for _, key := range input {
						reader.route([]byte{key})
					}
				} else {
					reader.route(input)
				}
				buffer := make([]byte, len(input))
				if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != string(input) || calls != 1 {
					t.Fatalf("editor input = %q, calls = %d, err = %v", buffer, calls, err)
				}
			})
		}
	}
}
