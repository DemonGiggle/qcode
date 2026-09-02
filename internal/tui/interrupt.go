package tui

import (
	"context"
	"io"
	"sync"
)

const (
	ctrlC = byte(3)
	ctrlU = byte(21)

	pageUpSequence   = "\x1b[5~"
	pageDownSequence = "\x1b[6~"
)

// interruptReader owns terminal input so Ctrl+C can cancel an active agent
// task even while the line editor is not reading. Other input typed while a
// task is active is discarded instead of leaking into the next prompt.
type interruptReader struct {
	source io.Reader
	data   chan byte
	once   sync.Once

	mu      sync.Mutex
	cancel  context.CancelFunc
	page    func(int)
	err     error
	pending []byte
}

func newInterruptReader(source io.Reader) *interruptReader {
	return &interruptReader{source: source, data: make(chan byte, 256)}
}

func (r *interruptReader) start() {
	r.once.Do(func() { go r.readLoop() })
}

func (r *interruptReader) setCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()
}

func (r *interruptReader) setPageHandler(page func(int)) {
	r.mu.Lock()
	r.page = page
	r.mu.Unlock()
}

func (r *interruptReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	first, ok := <-r.data
	if !ok {
		r.mu.Lock()
		err := r.err
		r.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}
	buffer[0] = first
	count := 1
	for count < len(buffer) {
		select {
		case next, open := <-r.data:
			if !open {
				return count, nil
			}
			buffer[count] = next
			count++
		default:
			return count, nil
		}
	}
	return count, nil
}

func (r *interruptReader) readLoop() {
	defer close(r.data)
	buffer := make([]byte, 256)
	for {
		count, err := r.source.Read(buffer)
		if count > 0 {
			r.route(buffer[:count])
		}
		if err != nil {
			r.mu.Lock()
			r.err = err
			r.mu.Unlock()
			return
		}
	}
}

func (r *interruptReader) route(input []byte) {
	r.mu.Lock()
	cancel := r.cancel
	if cancel != nil {
		r.pending = nil
		for _, key := range input {
			if key == ctrlC {
				cancel()
				break
			}
		}
		r.mu.Unlock()
		return
	}
	page := r.page
	r.mu.Unlock()

	input = append(r.pending, input...)
	r.pending = nil
	for len(input) > 0 {
		if len(input) >= len(pageUpSequence) && string(input[:len(pageUpSequence)]) == pageUpSequence {
			if page != nil {
				page(1)
			}
			input = input[len(pageUpSequence):]
			continue
		}
		if len(input) >= len(pageDownSequence) && string(input[:len(pageDownSequence)]) == pageDownSequence {
			if page != nil {
				page(-1)
			}
			input = input[len(pageDownSequence):]
			continue
		}
		if isPageSequencePrefix(input) {
			r.pending = append(r.pending, input...)
			return
		}
		key := input[0]
		input = input[1:]
		if key == ctrlC {
			// Clear the current input and submit an empty line. x/term treats a
			// raw Ctrl+C as EOF, which would otherwise exit the application.
			r.data <- ctrlU
			r.data <- '\r'
			continue
		}
		r.data <- key
	}
}

func isPageSequencePrefix(input []byte) bool {
	if len(input) >= len(pageUpSequence) {
		return false
	}
	prefix := string(input)
	return pageUpSequence[:len(input)] == prefix || pageDownSequence[:len(input)] == prefix
}
