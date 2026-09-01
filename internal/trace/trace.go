package trace

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

const timestampLayout = "15:04:05"

var spinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Logger struct {
	out      io.Writer
	json     bool
	animated bool
	verbose  bool
	mu       sync.Mutex
}

type Span struct {
	logger  *Logger
	kind    string
	name    string
	start   time.Time
	fields  map[string]any
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	hidden  bool
	ended   bool
	verbose bool
}

func New(out io.Writer, jsonOutput bool) *Logger {
	return &Logger{out: out, json: jsonOutput, verbose: true}
}

// NewAnimated creates a logger that updates an in-progress text event in place.
// JSON output remains a pair of start/end events for machine consumers.
func NewAnimated(out io.Writer, jsonOutput bool) *Logger {
	return &Logger{out: out, json: jsonOutput, animated: !jsonOutput, verbose: true}
}

func (l *Logger) SetVerbose(verbose bool) {
	l.mu.Lock()
	l.verbose = verbose
	l.mu.Unlock()
}

func (l *Logger) Start(kind, name string, fields map[string]any) *Span {
	now := time.Now()
	l.mu.Lock()
	verbose := l.verbose
	l.mu.Unlock()
	s := &Span{logger: l, kind: kind, name: name, start: now, fields: cloneFields(fields), verbose: verbose}
	if l.json {
		l.writeJSON("start", kind, name, now, 0, fields)
		return s
	}
	if l.animated {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		l.writeProgress(s, spinnerFrames[0])
		go s.animate()
	}
	return s
}

// Suspend clears an animated event before another writer starts producing
// terminal output. End will print the completed event after that output.
func (s *Span) Suspend() {
	if s == nil || !s.logger.animated {
		return
	}
	s.stopAnimation()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended || s.hidden {
		return
	}
	s.logger.mu.Lock()
	fmt.Fprint(s.logger.out, "\r\x1b[2K")
	s.logger.mu.Unlock()
	s.hidden = true
}

func (s *Span) End(err error) {
	if s == nil {
		return
	}
	s.stopAnimation()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	fields := cloneFields(s.fields)
	if err != nil {
		fields["error"] = err.Error()
	}
	duration := time.Since(s.start)
	if s.logger.json {
		endFields := map[string]any{}
		if err != nil {
			endFields["error"] = err.Error()
		}
		s.logger.writeJSON("end", s.kind, s.name, time.Now(), duration, endFields)
		return
	}
	if s.logger.animated && !s.verbose {
		s.logger.clearProgress(!s.hidden)
		return
	}
	s.logger.writeCompleted(s, duration, fields, !s.hidden)
}

func (s *Span) animate() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	defer close(s.done)
	frame := 1
	for {
		select {
		case <-ticker.C:
			s.logger.writeProgress(s, spinnerFrames[frame%len(spinnerFrames)])
			frame++
		case <-s.stop:
			return
		}
	}
}

func (s *Span) stopAnimation() {
	if s.stop == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

func (l *Logger) writeProgress(s *Span, frame string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !s.verbose {
		fmt.Fprintf(l.out, "\rWorking (%s)", frame)
		return
	}
	fmt.Fprintf(l.out, "\r[%s] start %s %s (%s)", formatTimestamp(s.start), s.kind, s.name, frame)
	writeTextFields(l.out, s.fields)
}

func (l *Logger) clearProgress(clearLine bool) {
	if !clearLine {
		return
	}
	l.mu.Lock()
	fmt.Fprint(l.out, "\r\x1b[2K")
	l.mu.Unlock()
}

func (l *Logger) writeCompleted(s *Span, duration time.Duration, fields map[string]any, clearLine bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if clearLine {
		fmt.Fprint(l.out, "\r\x1b[2K")
	}
	fmt.Fprintf(l.out, "[%s] start %s %s (%s)", formatTimestamp(s.start), s.kind, s.name, duration.Round(time.Millisecond))
	writeTextFields(l.out, fields)
	fmt.Fprintln(l.out)
}

func (l *Logger) writeJSON(event, kind, name string, at time.Time, duration time.Duration, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := map[string]any{"timestamp": formatTimestamp(at), "event": event, "kind": kind, "name": name}
	if duration > 0 {
		entry["duration_ms"] = float64(duration.Microseconds()) / 1000
	}
	for key, value := range fields {
		entry[key] = value
	}
	data, _ := json.Marshal(entry)
	fmt.Fprintln(l.out, string(data))
}

func writeTextFields(out io.Writer, fields map[string]any) {
	if value, ok := fields["arguments"]; ok {
		fmt.Fprintf(out, " arguments=%v", value)
	}
	if value, ok := fields["error"]; ok {
		fmt.Fprintf(out, " error=%v", value)
	}
}

func cloneFields(fields map[string]any) map[string]any {
	cloned := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}

func formatTimestamp(at time.Time) string {
	return at.In(time.Local).Format(timestampLayout)
}
