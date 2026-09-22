package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/session"
)

func TestPrettyExportGroupsAndOrdersCompletedWork(t *testing.T) {
	stamp := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	records := []session.WorkRecord{
		{RequestID: "request-1", AgentID: "main", Prompt: "Later", Status: "completed", Created: stamp.Add(time.Hour)},
		{RequestID: "request-2", AgentID: "main", Prompt: "First", Status: "completed", Created: stamp},
		{RequestID: "request-3", AgentID: "main", Prompt: "Same time", Status: "completed", Created: stamp},
		{RequestID: "request-4", AgentID: "agent-2", AgentName: "Review", Prompt: "Closed work", Status: "completed", Created: stamp},
		{RequestID: "request-5", AgentID: "agent-3", Prompt: "Consultation", Status: "completed", Consultation: true},
	}
	for _, status := range []string{"running", "queued", "failed", "cancelled", "interrupted", "timed_out"} {
		records = append(records, session.WorkRecord{AgentID: "main", Prompt: status, Status: status})
	}
	original := append([]session.WorkRecord(nil), records...)
	got := prettyExportAgents([]session.Summary{{ID: "agent-10", Name: "Empty"}, {ID: "main", Name: "Main"}}, records, "agent-10")
	if len(got) != 3 || got[0].ID != "main" || got[1].ID != "agent-2" || got[2].ID != "agent-10" {
		t.Fatalf("agent order: %+v", got)
	}
	if !got[1].Closed || got[1].Name != "Review" || !got[2].Active || len(got[2].Records) != 0 {
		t.Fatalf("agent metadata: %+v", got)
	}
	var prompts []string
	for _, r := range got[0].Records {
		prompts = append(prompts, r.Prompt)
	}
	if !reflect.DeepEqual(prompts, []string{"First", "Same time", "Later"}) {
		t.Fatalf("prompts: %v", prompts)
	}
	if !reflect.DeepEqual(records, original) {
		t.Fatal("export changed the source journal")
	}
	if fallback := prettyExportAgents(nil, records, "missing"); !fallback[0].Active {
		t.Fatal("missing initial tab fallback")
	}
}

func TestPrettyExportMarkdownAndEscaping(t *testing.T) {
	markdown := "# Result\n\n**Done** and `code`\n\n- item\n\n| Name | Value |\n| --- | --- |\n| x | 1 |\n\n```go\nfmt.Println(\"<tag>\")\n```\n\n[Docs](https://example.com)\n\n![preview](https://example.com/image.png)\n\n<script>alert('bad')</script>\n\n[bad](javascript:alert(1))"
	record := session.WorkRecord{AgentID: "main", Status: "completed", Prompt: "<script>prompt</script>\nsecond line & 日本語", Response: markdown, Model: "model <one>"}
	data, err := renderPrettyExport("/workspace/<name>", "main", time.Now(), []session.Summary{{ID: "main", Name: "Main <agent>"}}, []session.WorkRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"&lt;script&gt;prompt&lt;/script&gt;\nsecond line &amp; 日本語", "Main &lt;agent&gt;", "model &lt;one&gt;", "<h1>Result</h1>", "<strong>Done</strong>", "<table>", "<li>item</li>", "language-go", "&lt;tag&gt;", `href="https://example.com"`, "Image: preview", "Time unavailable", "Exchange 01"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"<script>prompt", "alert('bad')", `href="javascript:`, "<img", "\x1b"} {
		if strings.Contains(got, bad) {
			t.Errorf("unsafe or unwanted content %q", bad)
		}
	}
}

func TestPrettyExportEmptyStates(t *testing.T) {
	for _, records := range [][]session.WorkRecord{nil, {{AgentID: "main", Status: "completed"}}} {
		data, err := renderPrettyExport("workspace", "main", time.Now(), []session.Summary{{ID: "main"}}, records)
		if err != nil {
			t.Fatal(err)
		}
		want := "No response text."
		if records == nil {
			want = "/export raw"
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing empty state %q", want)
		}
	}
}

func TestExportModesAndGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	u := &UI{root: root, manager: &historyBrowserManager{records: []session.WorkRecord{{AgentID: "main", Status: "completed", Prompt: "Saved prompt", Response: "Saved response"}}}}
	for _, mode := range []string{"", "pretty", "raw"} {
		doc, err := u.GenerateExport(mode)
		if err != nil {
			t.Fatal(err)
		}
		expectedMode := mode
		if expectedMode == "" {
			expectedMode = "pretty"
		}
		if !strings.HasPrefix(doc.Filename, "qcode-session-"+expectedMode+"-") || filepath.Base(doc.Filename) != doc.Filename || !strings.HasSuffix(doc.Filename, ".html") {
			t.Fatalf("filename %q", doc.Filename)
		}
		if mode != "raw" && !bytes.Contains(doc.Data, []byte("Saved response")) {
			t.Fatal("pretty export lost response")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only export wrote files: %v, %v", entries, err)
	}
	for _, mode := range []string{"report.html", "/tmp/export.html", "pretty report.html", "raw pretty", "timeline", "--mode pretty"} {
		if _, err := u.exportSession(mode); !errors.Is(err, ErrExportUsage) {
			t.Fatalf("argument %q: %v", mode, err)
		}
	}
	entries, _ = os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("invalid command wrote files")
	}
	first, err := u.exportSession("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := u.exportSession("pretty")
	if err != nil || first == second {
		t.Fatalf("repeated export paths %q %q: %v", first, second, err)
	}
	if filepath.Dir(first) != root {
		t.Fatal("export escaped workspace")
	}
	data, err := os.ReadFile(first)
	if err != nil || !bytes.Contains(data, []byte("Saved prompt")) {
		t.Fatalf("saved document: %v", err)
	}
}

func TestPrettyExportRestoredJournal(t *testing.T) {
	manager := agent.NewAgentManager(context.Background(), 20)
	defer manager.Shutdown()
	err := manager.RestoreWorkHistory(&session.WorkHistory{NextRequestID: 1, Records: []session.WorkRecord{{RequestID: "request-1", AgentID: "main", Status: "completed", Prompt: "Before restart", Response: "Durable answer"}}})
	if err != nil {
		t.Fatal(err)
	}
	u := &UI{manager: manager, root: t.TempDir()}
	doc, err := u.GenerateExport("pretty")
	if err != nil || !bytes.Contains(doc.Data, []byte("Durable answer")) {
		t.Fatalf("restored export: %v", err)
	}
}

func TestRawExportIncludesClearedAndEvictedOutput(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := New(out, out, statusRunner{}, "test", "model", t.TempDir())
	u.AddAgentView("main", "test", "model")
	display := u.views["main"].display
	display.AddLine("oldest archived output")
	for i := 0; i < maxHistoryLines+1; i++ {
		display.AddLine("padding")
	}
	display.Clear()
	display.AddLine("after clear")
	doc, err := u.GenerateExport("raw")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oldest archived output", "after clear"} {
		if !bytes.Contains(doc.Data, []byte(want)) {
			t.Fatalf("raw export missing %q", want)
		}
	}
}
