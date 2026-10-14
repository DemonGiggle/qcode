package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"qcode/internal/session"
)

// Exercise the actual fixed-screen switch path while excluding terminal I/O
// latency. Warm both tabs, as a user revisiting an existing conversation does.
func BenchmarkSwitchAgent(b *testing.B) {
	benchmarkSwitchAgent(b, false)
}

// Background streaming changes only the unfinished line before each switch.
func BenchmarkSwitchAgentAfterOutput(b *testing.B) {
	benchmarkSwitchAgent(b, true)
}

func benchmarkSwitchAgent(b *testing.B, streaming bool) {
	for _, n := range []int{50, 500, 5000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
			if err != nil {
				b.Fatal(err)
			}
			defer out.Close()
			u := New(nil, out, statusRunner{}, "test", "model", b.TempDir())
			m := &persistenceManager{}
			for _, id := range []string{"main", "agent-1"} {
				u.AddAgentView(id, "test", "model")
				m.agents = append(m.agents, session.SavedAgent{Summary: session.Summary{ID: id, Name: id, Status: session.StatusIdle}})
				for i := 0; i < n; i++ {
					u.views[id].display.AddLine("\x1b[36m" + strings.Repeat("Example output with code, text and 日本語. ", 3) + reset)
				}
			}
			u.SetDetachedAgentManager(m)
			u.fixedInput = true
			for _, id := range []string{"main", "agent-1"} {
				if err := u.switchAgent(id); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := "main"
				if i%2 != 0 {
					id = "agent-1"
				}
				if streaming {
					_, _ = io.WriteString(u.views[id].display, fmt.Sprintf("\r\x1b[2Kprogress %d", i))
				}
				if err := u.switchAgent(id); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
