package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"qcode/internal/question"
	"qcode/internal/redaction"
	"qcode/internal/session"
)

const tuiTestSecret = "synthetic-ui-credential"

type redactionManager struct {
	*persistenceManager
	records      []session.WorkRecord
	queued       []session.QueuedPrompt
	interactions []session.Interaction
	resolution   session.Resolution
}

func (m *redactionManager) WorkRecords() []session.WorkRecord           { return m.records }
func (m *redactionManager) QueuedPrompts(string) []session.QueuedPrompt { return m.queued }
func (m *redactionManager) PendingInteractions() []session.Interaction  { return m.interactions }
func (m *redactionManager) BeginInteraction(session.Interaction) (session.InteractionWaiter, error) {
	return nil, errors.New("unused")
}
func (m *redactionManager) ResolveInteraction(r session.Resolution) error {
	m.resolution = r
	return nil
}

func TestTerminalRemoteExportsAndClearRedactIndependentCopies(t *testing.T) {
	u, base := persistenceUI(t)
	disabled := false
	policy, _ := redaction.New(redaction.Config{Terminal: &disabled}, []string{tuiTestSecret})
	u.SetRedaction(policy)
	manager := &redactionManager{persistenceManager: base, records: []session.WorkRecord{{AgentID: "main", RequestID: "request-1", Prompt: tuiTestSecret, Response: "response " + tuiTestSecret, Status: "completed", Activities: []session.WorkActivity{{Summary: "shell " + tuiTestSecret}}}}, queued: []session.QueuedPrompt{{RequestID: "queue-1", Prompt: tuiTestSecret}}}
	u.SetDetachedAgentManager(manager)
	fmt.Fprintln(u.display, "old output "+tuiTestSecret)
	u.display.Clear()
	fmt.Fprintln(u.display, "new output "+tuiTestSecret)
	original := strings.Join(u.display.Lines(), "\n")
	for _, mode := range []string{"raw", "pretty"} {
		document, err := u.GenerateExport(mode)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(document.Data, []byte(tuiTestSecret)) || !bytes.Contains(document.Data, []byte(redaction.Marker)) {
			t.Fatalf("%s leaked", mode)
		}
		if mode == "raw" && !bytes.Contains(document.Data, []byte("old output")) {
			t.Fatal("clear discarded export archive")
		}
	}
	presentation := u.RemotePresentation()
	encoded, _ := json.Marshal(presentation)
	if bytes.Contains(encoded, []byte(tuiTestSecret)) || presentation.Views[1].QueuedPrompts[0].RequestID != "queue-1" {
		t.Fatal("remote leaked or routing changed")
	}
	if strings.Join(u.display.Lines(), "\n") != original || manager.records[0].Prompt != tuiTestSecret || manager.queued[0].Prompt != tuiTestSecret {
		t.Fatal("boundary changed live state")
	}
	// Terminal defaults also filter submitted history and system messages.
	enabled, _ := redaction.New(redaction.Config{}, []string{tuiTestSecret})
	u.SetRedaction(enabled)
	u.display.AddLine("> " + tuiTestSecret)
	u.printSystemMessage("provider failed: " + tuiTestSecret)
	lines := u.display.Lines()
	if strings.Contains(strings.Join(lines[len(lines)-4:], "\n"), tuiTestSecret) {
		t.Fatal("submitted text leaked")
	}
}
func TestPresentationFilterStyledCellsDraftsAndPartialCheckpoint(t *testing.T) {
	p, _ := redaction.New(redaction.Config{}, []string{tuiTestSecret})
	s := savedPresentation{Active: "main", Drafts: map[string]string{"main": tuiTestSecret}, Views: []savedView{{ID: "main", Buffer: tuiTestSecret, Diffs: []string{"+" + tuiTestSecret}, Table: []savedTableLine{{Text: tuiTestSecret}}, History: savedHistory{Lines: []string{tuiTestSecret}, Archive: []string{"-----BEGIN PRIVATE KEY-----", "PEM material", "-----END PRIVATE KEY-----"}, Pending: []byte(tuiTestSecret), Cursor: len(tuiTestSecret)}}}}
	for _, r := range tuiTestSecret {
		s.Views[0].History.Current = append(s.Views[0].History.Current, savedCell{Char: r, Style: "\x1b[31m"})
	}
	data, _ := json.Marshal(s)
	filtered, err := FilterPresentation(p, data)
	if err != nil {
		t.Fatal(err)
	}
	var result savedPresentation
	json.Unmarshal(filtered, &result)
	var cells strings.Builder
	for _, c := range result.Views[0].History.Current {
		cells.WriteRune(c.Char)
	}
	if cells.String() != redaction.Marker || result.Views[0].History.Cursor > len(result.Views[0].History.Current) || result.Views[0].Buffer != "" || len(result.Views[0].History.Pending) != 0 || bytes.Contains(filtered, []byte(tuiTestSecret)) || bytes.Contains(filtered, []byte("PEM material")) {
		t.Fatal("typed presentation leaked")
	}
	u, _ := persistenceUI(t)
	u.SetRedaction(p)
	response := u.views["main"].response
	response.BeginResponse()
	response.Write([]byte(tuiTestSecret[:10]))
	snapshot, _ := json.Marshal(u.snapshotPresentation())
	if bytes.Contains(snapshot, []byte(tuiTestSecret[:10])) {
		t.Fatal("mid-response checkpoint retained raw pending text")
	}
}
func TestNumberedRemoteQuestionAndDirectoryResolution(t *testing.T) {
	u, base := persistenceUI(t)
	p, _ := redaction.New(redaction.Config{}, []string{tuiTestSecret})
	u.SetRedaction(p)
	questions := []question.Question{{Text: "Choose " + tuiTestSecret, Options: []string{tuiTestSecret, "safe"}}}
	payload, _ := json.Marshal(questions)
	manager := &redactionManager{persistenceManager: base, interactions: []session.Interaction{{ID: "question-1", AgentID: "main", Kind: session.InteractionQuestions, Payload: payload}}}
	u.SetDetachedAgentManager(manager)
	snapshot := u.RemotePresentation()
	encoded, _ := json.Marshal(snapshot)
	if bytes.Contains(encoded, []byte(tuiTestSecret)) || snapshot.Interactions[0].ID != "question-1" {
		t.Fatal("interaction payload leaked")
	}
	if err := u.ResolveRemoteInteraction("browser", "question-1", []byte(`["1"]`)); err != nil {
		t.Fatal(err)
	}
	var answers []string
	json.Unmarshal(manager.resolution.Value, &answers)
	if answers[0] != tuiTestSecret {
		t.Fatal("number did not resolve against original options")
	}
	manager.interactions = []session.Interaction{{ID: "directory-1", Kind: session.InteractionDirectoryApproval, Payload: json.RawMessage(`{"requested":"/` + tuiTestSecret + `/file","proposed":"/` + tuiTestSecret + `"}`)}}
	if err := u.ResolveRemoteInteraction("browser", "directory-1", []byte(`{"selected":"","approved":true}`)); err != nil {
		t.Fatal(err)
	}
	var answer struct{ Selected string }
	json.Unmarshal(manager.resolution.Value, &answer)
	if answer.Selected != "/"+tuiTestSecret {
		t.Fatal("directory resolution used displayed label")
	}
}
func TestMarkdownRedactionRedirectedThinkingFinalAndOverflow(t *testing.T) {
	p, _ := redaction.New(redaction.Config{}, []string{tuiTestSecret})
	var out bytes.Buffer
	w := NewMarkdownWriter(&out, false, 0)
	w.SetRedaction(p)
	w.BeginResponse()
	w.BeginThinking()
	for _, b := range []byte(tuiTestSecret) {
		w.Write([]byte{b})
	}
	if out.Len() != 0 {
		t.Fatal("stream leaked before newline")
	}
	w.EndThinking()
	w.Write([]byte("\n" + tuiTestSecret))
	w.EndResponse()
	if strings.Contains(out.String(), tuiTestSecret) || strings.Count(out.String(), redaction.Marker) != 2 {
		t.Fatalf("redirected output: %q", out.String())
	}
	out.Reset()
	w.BeginResponse()
	w.Write([]byte(strings.Repeat("x", redaction.MaxPendingLine+1) + "\nsafe"))
	w.EndResponse()
	if out.String() != redaction.Marker+"\nsafe" {
		t.Fatal("overflow not discarded")
	}
}

func TestStyledCurrentRowWithinLegacyPEMIsRedacted(t *testing.T) {
	state := savedPresentation{Views: []savedView{{History: savedHistory{Lines: []string{"-----BEGIN PRIVATE KEY-----"}, Archive: []string{"-----BEGIN PRIVATE KEY-----"}, Cursor: 12}}}}
	for _, r := range "raw PEM body" {
		state.Views[0].History.Current = append(state.Views[0].History.Current, savedCell{Char: r})
	}
	data, _ := json.Marshal(state)
	filtered, err := FilterPresentation(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	var got savedPresentation
	json.Unmarshal(filtered, &got)
	var row strings.Builder
	for _, cell := range got.Views[0].History.Current {
		row.WriteRune(cell.Char)
	}
	if row.String() != redaction.Marker {
		t.Fatal("PEM body survived in character cells")
	}
}

func TestPaymentThinkingIsFilteredBeforeMarkdownWrapping(t *testing.T) {
	const text = "**Number:** 4111 1111 1111 1111\n**CVV:** `123`\n**Valid Thru:** 04/29"
	for _, styled := range []bool{false, true} {
		var out bytes.Buffer
		// A narrow width would separate a raw PAN before a post-render matcher
		// could recognize it; filtering must happen before wrapping.
		writer := NewMarkdownWriter(&out, styled, 16)
		writer.BeginResponse()
		writer.BeginThinking()
		for _, b := range []byte(text) {
			writer.Write([]byte{b})
		}
		writer.EndThinking()
		writer.EndResponse()
		for _, sensitive := range []string{"4111", "1111", "123", "04/29"} {
			if strings.Contains(out.String(), sensitive) {
				t.Fatalf("styled=%t: payment data survived Markdown rendering", styled)
			}
		}
	}
}
