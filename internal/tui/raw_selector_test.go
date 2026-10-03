package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"qcode/internal/lineedit"
	"qcode/internal/session"
)

func TestRawSelectorUsesOnlyOutputViewport(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := &UI{fixedInput: true, out: out, width: 80, height: 24}
	bottom := u.height - 1 - u.statusLinesLocked()
	u.beginRawSelector()
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, fmt.Sprintf("\x1b[2;%dr\x1b[2;1H", bottom)) {
		t.Fatalf("selector region = %q", got)
	}
	if !strings.Contains(got, "Ctrl+C closes view") {
		t.Fatalf("selector did not show its own close instruction: %q", got)
	}
}

func TestCommandViewFooterStaysStableDuringStatusUpdates(t *testing.T) {
	for _, initialRows := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d status rows", initialRows), func(t *testing.T) {
			u, _ := layoutFixture(t)
			u.statusActive = true
			u.provider, u.model, u.root, u.width, u.unicode = "ollama", "qwen", "/w", 40, true
			if initialRows == 1 {
				u.SetStatuslineHidden([]string{"ctx", "tok"})
			}
			if got := u.statusLinesLocked(); got != initialRows {
				t.Fatalf("initial status rows = %d, want %d", got, initialRows)
			}
			u.beginRawSelector()
			visible := u.commandViewHeightLocked()
			before := fileSize(t, u.out)
			if initialRows == 1 {
				u.SetStatuslineHidden(nil)
			} else {
				u.SetStatuslineHidden([]string{"ctx", "tok"})
			}
			u.drawStatusBar()
			data, err := os.ReadFile(u.out.Name())
			if err != nil {
				t.Fatal(err)
			}
			update := string(data[before:])
			if strings.Contains(update, "\x1b[2;") || u.commandViewHeightLocked() != visible {
				t.Fatal("status update moved the command view's cursor or scroll boundary")
			}
			if statusBarLineCount(u.statusBarText) != initialRows || !strings.Contains(update, "Ctrl+C closes view") {
				t.Fatal("status update displaced the command view's footer")
			}
			u.endRawSelector()
			if got := u.statusLinesLocked(); got != 3-initialRows {
				t.Fatalf("restored status rows = %d, want %d", got, 3-initialRows)
			}
		})
	}
}

type commandViewManager struct {
	*layoutManager
	records   []session.WorkRecord
	cancelled bool
}

func (m *commandViewManager) WorkRecords() []session.WorkRecord { return m.records }
func (m *commandViewManager) Cancel(string) error {
	m.cancelled = true
	return nil
}

func TestCommandViewsHideConversationControlsAndKeepAgentRunning(t *testing.T) {
	for _, view := range []struct {
		name, marker string
		open         func(*UI) error
	}{
		{"history", "History: main", func(u *UI) error { u.showHistory(context.Background()); return nil }},
		{"plan", "Plan view", func(u *UI) error { return u.showPlanView(context.Background(), "a plan") }},
		{"skill draft", "Skill draft", func(u *UI) error { return u.showSkillPlanView(context.Background(), "a skill draft") }},
		{"remote", "Remote control active", func(u *UI) error {
			return u.showRemoteActive(RemoteStatus{Running: true, Mode: RemoteModeTailscale, URL: "https://example.test"})
		}},
	} {
		t.Run(view.name, func(t *testing.T) {
			u, _ := layoutFixture(t)
			manager := &commandViewManager{layoutManager: u.manager.(*layoutManager), records: []session.WorkRecord{
				historyRecord("request-1", "main", "old prompt", "old response", "completed", false, 1),
			}}
			manager.latest = map[string]string{"main": "current task\nsecond pinned row\nthird pinned row"}
			u.manager = manager
			u.input = newInterruptReader(nil)
			u.terminal = lineedit.NewTerminal(readWriter{Reader: u.input, Writer: u.out}, inputPrompt)
			u.terminal.RenderInput = u.renderInput
			u.input.setCancel(func() { _ = manager.Cancel("main") })
			for i := 0; i < 50; i++ {
				u.display.AddLine("conversation history")
			}
			u.renderInput(inputPrompt, "unfinished draft", 4)
			u.showPage(1)
			beforeViewport := *u.activeViewportLocked()
			before := fileSize(t, u.out)
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() { defer close(finished); result <- view.open(u) }()
			t.Cleanup(func() {
				select {
				case <-finished:
					return
				default:
				}
				u.input.route([]byte{ctrlC})
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("command view did not close")
				}
			})
			waitCommandView(t, u, view.marker)
			u.drawTaskIndicator()
			u.drawStatusBar()
			_, _ = io.WriteString(u.display, "background output\n")
			data, err := os.ReadFile(u.out.Name())
			if err != nil {
				t.Fatal(err)
			}
			modal := string(data[before:])
			for _, text := range []string{"current task", "(Steer)>", "Waiting (", "History paused"} {
				if strings.Contains(modal, text) {
					t.Fatalf("conversation control %q appeared inside command view:\n%s", text, modal)
				}
			}
			for row := 2; row <= u.height-u.statusLinesLocked(); row++ {
				if !strings.Contains(modal, fmt.Sprintf("\x1b[%d;1H\x1b[2K", row)) {
					t.Fatalf("command view did not clear conversation row %d", row)
				}
			}
			if !strings.Contains(modal, "Ctrl+C closes view") {
				t.Fatal("command view did not explain Ctrl+C")
			}
			if view.name == "history" {
				u.input.route([]byte("\r"))
				waitCommandView(t, u, "History item 1/1")
			}
			u.input.route([]byte{ctrlC})
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Ctrl+C did not close the command view")
			}
			if manager.cancelled || manager.states["main"] != session.StatusRunning {
				t.Fatal("closing the command view cancelled the running agent")
			}
			if u.inputText != "unfinished draft" || u.inputPosition != 4 || u.activeViewportLocked().anchor != beforeViewport.anchor || !u.activeViewportLocked().browsing {
				t.Fatal("closing the command view lost the draft or history position")
			}
			if !strings.Contains(questionnaireFrame(u), "current task") || !strings.Contains(questionnaireFrame(u), "(Steer)> unfinished draft") {
				t.Fatal("closing the command view did not restore conversation controls")
			}
		})
	}
}

func waitCommandView(t *testing.T, u *UI, marker string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		u.screenMu.Lock()
		data, err := os.ReadFile(u.out.Name())
		u.screenMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("command view did not show %q", marker)
		}
		time.Sleep(time.Millisecond)
	}
}
