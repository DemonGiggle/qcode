package trace

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

const timestampLayout = "15:04:05"

type Logger struct {
	out  io.Writer
	json bool
	mu   sync.Mutex
}

type Span struct {
	logger *Logger
	kind   string
	name   string
	start  time.Time
	ended  bool
}

func New(out io.Writer, jsonOutput bool) *Logger { return &Logger{out: out, json: jsonOutput} }

func (l *Logger) Start(kind, name string, fields map[string]any) *Span {
	now := time.Now()
	l.write("start", kind, name, now, 0, fields)
	return &Span{logger: l, kind: kind, name: name, start: now}
}

func (s *Span) End(err error) {
	if s == nil || s.ended {
		return
	}
	s.ended = true
	fields := map[string]any{}
	if err != nil {
		fields["error"] = err.Error()
	}
	s.logger.write("end", s.kind, s.name, time.Now(), time.Since(s.start), fields)
}

func (l *Logger) write(event, kind, name string, at time.Time, duration time.Duration, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	timestamp := formatTimestamp(at)
	if l.json {
		entry := map[string]any{"timestamp": timestamp, "event": event, "kind": kind, "name": name}
		if duration > 0 {
			entry["duration_ms"] = float64(duration.Microseconds()) / 1000
		}
		for key, value := range fields {
			entry[key] = value
		}
		data, _ := json.Marshal(entry)
		fmt.Fprintln(l.out, string(data))
		return
	}
	if duration > 0 {
		fmt.Fprintf(l.out, "[%s] %s %s %s (%s)", timestamp, event, kind, name, duration.Round(time.Millisecond))
	} else {
		fmt.Fprintf(l.out, "[%s] %s %s %s", timestamp, event, kind, name)
	}
	if value, ok := fields["arguments"]; ok {
		fmt.Fprintf(l.out, " arguments=%v", value)
	}
	if value, ok := fields["error"]; ok {
		fmt.Fprintf(l.out, " error=%v", value)
	}
	fmt.Fprintln(l.out)
}

func formatTimestamp(at time.Time) string {
	return at.In(time.Local).Format(timestampLayout)
}
