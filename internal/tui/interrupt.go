package tui

import (
	"context"
	"io"
	"sync"
	"time"
)

const (
	ctrlC = byte(3)
	ctrlU = byte(21)

	pageUpSequence       = "\x1b[5~"
	pageDownSequence     = "\x1b[6~"
	ctrlPageUpSequence   = "\x1b[5;5~"
	ctrlPageDownSequence = "\x1b[6;5~"
	rxvtCtrlPageUp       = "\x1b[5^"
	rxvtCtrlPageDown     = "\x1b[6^"
	altPreviousTab       = "\x1b,"
	altNextTab           = "\x1b."
	altQueuePanel        = "\x1bq"
)

type tabKeySequence struct {
	value     string
	direction int
}

var tabKeySequences = []tabKeySequence{
	{value: ctrlPageUpSequence, direction: -1},
	{value: ctrlPageDownSequence, direction: 1},
	{value: rxvtCtrlPageUp, direction: -1},
	{value: rxvtCtrlPageDown, direction: 1},
	{value: altPreviousTab, direction: -1},
	{value: altNextTab, direction: 1},
}

// interruptReader owns terminal input so Ctrl+C can cancel an active agent
// task while ordinary input remains available for queued prompts.
type interruptReader struct {
	source   io.Reader
	data     chan byte
	injected chan byte
	rawWake  chan byte
	once     sync.Once

	mu       sync.Mutex
	injectMu sync.Mutex
	cancel   context.CancelFunc
	page     func(int)
	tab      func(int)
	queue    func()
	err      error
	pending  []byte
	raw      bool
}

func newInterruptReader(source io.Reader) *interruptReader {
	return &interruptReader{source: source, data: make(chan byte, 256), injected: make(chan byte, 256), rawWake: make(chan byte, 1)}
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

func (r *interruptReader) setTabHandler(tab func(int)) {
	r.mu.Lock()
	r.tab = tab
	r.mu.Unlock()
}

func (r *interruptReader) setQueueHandler(toggle func()) {
	r.mu.Lock()
	r.queue = toggle
	r.mu.Unlock()
}

func (r *interruptReader) setRaw(raw bool) {
	r.mu.Lock()
	r.raw = raw
	r.pending = nil
	r.mu.Unlock()
	if !raw {
		for {
			select {
			case <-r.rawWake:
			default:
				return
			}
		}
	}
}

const selectorEscapeWait = 30 * time.Millisecond

// readSelectorEscapeTail disambiguates a standalone Escape key from terminal
// escape sequences. Sequence bytes are normally already buffered together;
// the short wait matters only when Escape has no tail.
func (r *interruptReader) readSelectorEscapeTail() []byte {
	read := func() (byte, bool) {
		timer := time.NewTimer(selectorEscapeWait)
		defer timer.Stop()
		r.mu.Lock()
		raw := r.raw
		r.mu.Unlock()
		if raw {
			select {
			case value, open := <-r.data:
				return value, open
			case value := <-r.rawWake:
				return value, true
			case <-timer.C:
				return 0, false
			}
		}
		select {
		case value, open := <-r.data:
			return value, open
		case value := <-r.injected:
			return value, true
		case <-timer.C:
			return 0, false
		}
	}

	first, ok := read()
	if !ok {
		return nil
	}
	tail := []byte{first}
	if first != '[' {
		return tail
	}
	second, ok := read()
	if !ok {
		return tail
	}
	tail = append(tail, second)
	if second != '5' && second != '6' {
		return tail
	}
	if terminator, ok := read(); ok {
		tail = append(tail, terminator)
	}
	return tail
}

// wakeRaw dismisses a raw selector without placing a synthetic key into the
// normal line editor. Browser prompts use it to close the /remote panel.
func (r *interruptReader) wakeRaw() {
	r.mu.Lock()
	raw := r.raw
	r.mu.Unlock()
	if !raw {
		return
	}
	select {
	case r.rawWake <- 0:
	default:
	}
}

func (r *interruptReader) interruptLine() {
	r.data <- ctrlU
	r.data <- '\r'
}

func (r *interruptReader) inject(data []byte) {
	go func() {
		r.injectMu.Lock()
		defer r.injectMu.Unlock()
		for _, key := range data {
			r.injected <- key
		}
	}()
}

func (r *interruptReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	first, source, ok := r.nextByte()
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
		case next, open := <-source:
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

// nextByte keeps programmatic input separate from terminal keystrokes while a
// raw selector is active. Browser prompts injected during a selector wait for
// the normal line editor instead of becoming selector navigation keys.
func (r *interruptReader) nextByte() (byte, <-chan byte, bool) {
	r.mu.Lock()
	raw := r.raw
	r.mu.Unlock()
	if raw {
		select {
		case key, ok := <-r.data:
			return key, r.data, ok
		case key := <-r.rawWake:
			return key, r.rawWake, true
		}
	}
	select {
	case key, ok := <-r.data:
		return key, r.data, ok
	case key := <-r.injected:
		return key, r.injected, true
	}
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
	if r.raw {
		r.pending = nil
		for _, key := range input {
			r.data <- key
		}
		r.mu.Unlock()
		return
	}
	cancel := r.cancel
	page := r.page
	tab := r.tab
	queue := r.queue
	pending := append([]byte(nil), r.pending...)
	r.pending = nil
	r.mu.Unlock()

	input = append(pending, input...)
	for len(input) > 0 {
		if len(input) >= len(altQueuePanel) && string(input[:len(altQueuePanel)]) == altQueuePanel {
			if queue != nil {
				queue()
			}
			input = input[len(altQueuePanel):]
			continue
		}
		if direction, length, ok := matchTabKeySequence(input); ok {
			if tab != nil {
				tab(direction)
			}
			input = input[length:]
			continue
		}
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
		if isKnownSequencePrefix(input) {
			r.mu.Lock()
			r.pending = append(r.pending, input...)
			r.mu.Unlock()
			return
		}
		key := input[0]
		input = input[1:]
		if cancel != nil {
			if key == ctrlC {
				cancel()
				return
			}
		}
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

func matchTabKeySequence(input []byte) (direction, length int, ok bool) {
	for _, sequence := range tabKeySequences {
		if len(input) >= len(sequence.value) && string(input[:len(sequence.value)]) == sequence.value {
			return sequence.direction, len(sequence.value), true
		}
	}
	return 0, 0, false
}

func isKnownSequencePrefix(input []byte) bool {
	prefix := string(input)
	if len(prefix) < len(altQueuePanel) && altQueuePanel[:len(prefix)] == prefix {
		return true
	}
	for _, sequence := range []string{pageUpSequence, pageDownSequence} {
		if len(prefix) < len(sequence) && sequence[:len(prefix)] == prefix {
			return true
		}
	}
	for _, sequence := range tabKeySequences {
		if len(prefix) < len(sequence.value) && sequence.value[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
