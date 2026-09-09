package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderSessionHTMLPreservesStylesAndEscapesText(t *testing.T) {
	views := []exportView{
		{
			id: "main", name: "main <agent>", provider: "demo", model: "model", status: "completed", active: true,
			history: historyExportSnapshot{lines: []string{
				"\x1b[31merror\x1b[0m & <details>",
				"\x1b[1;2;3;4;9mstyled\x1b[0m",
				"\x1b[38;5;60mgradient\x1b[0m",
			}, current: "visible"},
		},
		{id: "agent-1", name: "review", history: historyExportSnapshot{lines: []string{"background output"}}},
	}

	data := string(renderSessionHTML("/workspace/<demo>", "main", time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC), views))
	for _, want := range []string{
		"html, body { background: #000",
		"Workspace: /workspace/&lt;demo&gt;",
		"<h2 class=\"agent-heading\">main &lt;agent&gt;</h2>",
		`<span style="color:#800000">error</span> &amp; &lt;details&gt;`,
		`font-weight:700;opacity:.65;font-style:italic;text-decoration:underline line-through`,
		`<span style="color:#5f5f87">gradient</span>`,
		"visible",
		"background output",
	} {
		if !strings.Contains(data, want) {
			t.Fatalf("HTML missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(data, "\x1b[") {
		t.Fatalf("HTML contains raw ANSI sequence: %q", data)
	}
	if !strings.Contains(data, `class="agent active"`) {
		t.Fatal("active agent marker missing")
	}
}

func TestExportPathAndAtomicWrite(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.September, 9, 14, 32, 0, 0, time.Local)
	if got, want := exportPath(root, "", now), filepath.Join(root, "qcode-session-20260909-143200.html"); got != want {
		t.Fatalf("default export path = %q, want %q", got, want)
	}
	if got, want := exportPath(root, "reports/session.html", now), filepath.Join(root, "reports/session.html"); got != want {
		t.Fatalf("relative export path = %q, want %q", got, want)
	}
	abs := filepath.Join(root, "absolute.html")
	if got := exportPath(root, abs, now); got != abs {
		t.Fatalf("absolute export path = %q, want %q", got, abs)
	}

	path := filepath.Join(root, "session.html")
	if err := writeExportFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeExportFile(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("export file = %q, want %q", data, "second")
	}
}

func TestUIExportSessionCapturesAllAgentViews(t *testing.T) {
	root := t.TempDir()
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := New(out, out, statusRunner{}, "test", "main-model", root)
	mainDisplay, _ := u.AddAgentView("main", "test", "main-model")
	reviewDisplay, _ := u.AddAgentView("agent-1", "test", "review-model")
	_, _ = mainDisplay.Write([]byte("main output\n"))
	_, _ = reviewDisplay.Write([]byte("review output\n"))

	path, err := u.exportSession("transcript.html")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"main output", "review output", "main-model", "review-model"} {
		if !strings.Contains(text, want) {
			t.Fatalf("export missing %q:\n%s", want, text)
		}
	}
}
