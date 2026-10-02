package tui

import (
	"encoding/json"
	"strings"

	"qcode/internal/redaction"
	"qcode/internal/session"
)

// FilterSnapshot is injected into storage to avoid storage importing owners.
func FilterSnapshot(p *redaction.Policy, snap session.Snapshot) (session.Snapshot, error) {
	if !p.Enabled(redaction.Persistence) {
		return snap, nil
	}
	snap.Preview = p.Text(redaction.Persistence, snap.Preview)
	snap.Agents = append([]session.SavedAgent(nil), snap.Agents...)
	for i := range snap.Agents {
		item := &snap.Agents[i]
		item.Summary = redaction.Copy(p, redaction.Persistence, item.Summary)
		item.Summary.Name = p.Text(redaction.Persistence, item.Summary.Name)
	}
	if len(snap.Presentation) > 0 {
		filtered, err := FilterPresentation(p, snap.Presentation)
		if err != nil {
			return session.Snapshot{}, err
		}
		snap.Presentation = filtered
	}
	if snap.Work != nil {
		filtered := redaction.Copy(p, redaction.Persistence, *snap.Work)
		snap.Work = &filtered
	}
	return snap, nil
}

func filterLines(p *redaction.Policy, sink redaction.Sink, lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	return strings.Split(p.Text(sink, strings.Join(lines, "\n")), "\n")
}

// FilterPresentation reconstructs character cells before matching and drops
// unfinished streaming and terminal parser buffers from saved state.
func FilterPresentation(p *redaction.Policy, data json.RawMessage) (json.RawMessage, error) {
	var s savedPresentation
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if !p.Enabled(redaction.Persistence) {
		return append(json.RawMessage(nil), data...), nil
	}
	for id, draft := range s.Drafts {
		s.Drafts[id] = p.Text(redaction.Persistence, draft)
	}
	for i := range s.Views {
		v := &s.Views[i]
		h := &v.History
		var line strings.Builder
		for _, c := range h.Current {
			line.WriteRune(c.Char)
		}
		// The current cell row may be inside a PEM block begun in committed
		// history. Match the complete history before isolating this row.
		source := h.Archive
		if len(source) == 0 {
			source = h.Lines
		}
		combined := append(append([]string(nil), source...), line.String())
		safe := filterLines(p, redaction.Persistence, combined)
		filtered := safe[len(safe)-1]
		h.Lines = filterLines(p, redaction.Persistence, h.Lines)
		h.Archive = filterLines(p, redaction.Persistence, h.Archive)
		h.Style = p.Text(redaction.Persistence, h.Style)
		if filtered != line.String() {
			prefix := []rune(line.String())
			cursor := min(max(0, h.Cursor), len(prefix))
			h.Cursor = min(len([]rune(p.Text(redaction.Persistence, string(prefix[:cursor])))), len([]rune(filtered)))
			h.Current = nil
			for _, r := range filtered {
				h.Current = append(h.Current, savedCell{Char: r})
			}
		}
		h.Pending = nil
		v.Buffer = ""
		v.Diffs = filterLines(p, redaction.Persistence, v.Diffs)
		var table []string
		for _, line := range v.Table {
			table = append(table, line.Text)
		}
		table = filterLines(p, redaction.Persistence, table)
		for j := range v.Table {
			v.Table[j].Text = table[j]
		}
	}
	return json.Marshal(s)
}
