package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"qcode/internal/session"
)

type exportView struct {
	id       string
	name     string
	provider string
	model    string
	status   string
	active   bool
	history  historyExportSnapshot
}

// exportSession writes the current persistent presentation as a standalone
// HTML document. The snapshot is taken before the success message is added to
// the terminal, so a failed write cannot be mistaken for a completed export.
func (u *UI) exportSession(argument string) (string, error) {
	now := time.Now()
	path := exportPath(u.root, argument, now)
	views, active := u.exportViews()
	data := renderSessionHTML(u.root, active, now, views)
	if err := writeExportFile(path, data); err != nil {
		return path, err
	}
	return path, nil
}

func (u *UI) exportViews() ([]exportView, string) {
	var summaries []session.Summary
	if u.manager != nil {
		summaries = u.manager.List()
	}
	byID := make(map[string]session.Summary, len(summaries))
	orderedIDs := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
		orderedIDs = append(orderedIDs, summary.ID)
	}

	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	active := u.activeAgent
	seen := make(map[string]bool, len(orderedIDs))
	for _, id := range orderedIDs {
		if u.views[id] != nil {
			seen[id] = true
		}
	}
	// A detached or test UI may have views without a manager roster. Include
	// those views deterministically after the manager's stable order.
	var remaining []string
	for id := range u.views {
		if !seen[id] {
			remaining = append(remaining, id)
		}
	}
	sort.Strings(remaining)
	orderedIDs = append(orderedIDs, remaining...)

	views := make([]exportView, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		view := u.views[id]
		if view == nil || view.display == nil {
			continue
		}
		summary := byID[id]
		name := summary.Name
		if name == "" {
			name = id
		}
		views = append(views, exportView{
			id: id, name: name, provider: view.provider, model: view.model,
			status: string(summary.Status), active: id == active,
			history: view.display.ExportSnapshot(),
		})
	}
	return views, active
}

func exportPath(root, argument string, now time.Time) string {
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return filepath.Join(root, "qcode-session-"+now.Format("20060102-150405")+".html")
	}
	if filepath.IsAbs(argument) {
		return filepath.Clean(argument)
	}
	return filepath.Join(root, argument)
}

func writeExportFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".qcode-export-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func renderSessionHTML(workspace, active string, exportedAt time.Time, views []exportView) []byte {
	var output strings.Builder
	output.WriteString(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>qcode session export</title>
<style>
:root { color-scheme: dark; }
html, body { background: #000; color: #d7dde5; }
body { margin: 0; padding: 2rem; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace; }
h1 { color: #fff; font-size: 1.25rem; margin: 0 0 .35rem; }
.meta { color: #7f8a99; font-size: .85rem; margin: 0 0 2rem; }
.agent { border-top: 1px solid #303640; margin: 0 0 2rem; padding-top: 1rem; }
.agent.active { border-top-color: #22d3ee; }
.agent-heading { color: #f0f3f6; font-size: 1rem; margin: 0 0 .25rem; }
.agent-details { color: #7f8a99; font-size: .8rem; margin: 0 0 .75rem; }
.transcript { background: #000; border: 1px solid #20252d; border-radius: .35rem; color: #d7dde5; line-height: 1.4; margin: 0; overflow-x: auto; padding: 1rem; tab-size: 8; white-space: pre; }
.empty { color: #7f8a99; font-style: italic; }
</style>
</head>
<body>
<h1>qcode session export</h1>
<p class="meta">Workspace: `)
	output.WriteString(escapeHTML(workspace))
	output.WriteString(`<br>Exported: `)
	output.WriteString(escapeHTML(exportedAt.Format(time.RFC3339)))
	if active != "" {
		output.WriteString(`<br>Active tab: `)
		output.WriteString(escapeHTML(active))
	}
	output.WriteString(`</p>
`)

	if len(views) == 0 {
		output.WriteString(`<p class="empty">No agent transcript was captured.</p>
`)
	}
	for _, view := range views {
		class := "agent"
		if view.active {
			class += " active"
		}
		fmt.Fprintf(&output, `<section class="%s">`+"\n", class)
		fmt.Fprintf(&output, `<h2 class="agent-heading">%s</h2>`+"\n", escapeHTML(view.name))
		details := view.id
		if view.provider != "" || view.model != "" {
			details += " · " + view.provider + " / " + view.model
		}
		if view.status != "" {
			details += " · " + view.status
		}
		fmt.Fprintf(&output, `<p class="agent-details">%s</p>`+"\n", escapeHTML(details))
		transcript := renderExportTranscript(view.history)
		if transcript == "" {
			output.WriteString(`<p class="empty">No transcript captured.</p>`)
		} else {
			output.WriteString(`<pre class="transcript">`)
			output.WriteString(transcript)
			output.WriteString(`</pre>`)
		}
		output.WriteString("\n</section>\n")
	}
	output.WriteString(`</body>
</html>
`)
	return []byte(output.String())
}

func renderExportTranscript(snapshot historyExportSnapshot) string {
	lines := append([]string(nil), snapshot.lines...)
	if snapshot.current != "" {
		lines = append(lines, snapshot.current)
	}
	if len(lines) == 0 {
		return ""
	}
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, renderANSIHTML(line))
	}
	return strings.Join(rendered, "\n")
}

type exportANSIStyle struct {
	foreground string
	background string
	bold       bool
	dim        bool
	italic     bool
	underline  bool
	strike     bool
}

func (s exportANSIStyle) css() string {
	parts := make([]string, 0, 5)
	if s.foreground != "" {
		parts = append(parts, "color:"+s.foreground)
	}
	if s.background != "" {
		parts = append(parts, "background-color:"+s.background)
	}
	if s.bold {
		parts = append(parts, "font-weight:700")
	}
	if s.dim {
		parts = append(parts, "opacity:.65")
	}
	if s.italic {
		parts = append(parts, "font-style:italic")
	}
	decorations := make([]string, 0, 2)
	if s.underline {
		decorations = append(decorations, "underline")
	}
	if s.strike {
		decorations = append(decorations, "line-through")
	}
	if len(decorations) > 0 {
		parts = append(parts, "text-decoration:"+strings.Join(decorations, " "))
	}
	return strings.Join(parts, ";")
}

func (s *exportANSIStyle) apply(sequence string) {
	parameters := strings.TrimSuffix(strings.TrimPrefix(sequence, "\x1b["), "m")
	if parameters == "" {
		parameters = "0"
	}
	parts := strings.Split(parameters, ";")
	values := make([]int, len(parts))
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			value = 0
		}
		values[index] = value
	}
	for index := 0; index < len(values); index++ {
		code := values[index]
		switch {
		case code == 0:
			*s = exportANSIStyle{}
		case code == 1:
			s.bold = true
		case code == 2:
			s.dim = true
		case code == 3:
			s.italic = true
		case code == 4:
			s.underline = true
		case code == 9:
			s.strike = true
		case code == 22:
			s.bold, s.dim = false, false
		case code == 23:
			s.italic = false
		case code == 24:
			s.underline = false
		case code == 29:
			s.strike = false
		case code == 39:
			s.foreground = ""
		case code == 49:
			s.background = ""
		case code >= 30 && code <= 37:
			s.foreground = ansi16Color(code - 30)
		case code >= 90 && code <= 97:
			s.foreground = ansi16Color(code - 90 + 8)
		case code >= 40 && code <= 47:
			s.background = ansi16Color(code - 40)
		case code >= 100 && code <= 107:
			s.background = ansi16Color(code - 100 + 8)
		case code == 38 || code == 48:
			if index+2 < len(values) && values[index+1] == 5 {
				color := ansi256Color(values[index+2])
				if code == 38 {
					s.foreground = color
				} else {
					s.background = color
				}
				index += 2
			} else if index+4 < len(values) && values[index+1] == 2 {
				color := fmt.Sprintf("#%02x%02x%02x", clampByte(values[index+2]), clampByte(values[index+3]), clampByte(values[index+4]))
				if code == 38 {
					s.foreground = color
				} else {
					s.background = color
				}
				index += 4
			}
		}
	}
}

func renderANSIHTML(line string) string {
	var output strings.Builder
	var text strings.Builder
	style := exportANSIStyle{}
	flush := func() {
		if text.Len() == 0 {
			return
		}
		content := escapeHTML(text.String())
		if css := style.css(); css != "" {
			fmt.Fprintf(&output, `<span style="%s">%s</span>`, css, content)
		} else {
			output.WriteString(content)
		}
		text.Reset()
	}

	for index := 0; index < len(line); {
		if line[index] == '\x1b' {
			length, complete := ansiSequenceLength([]byte(line[index:]))
			if complete {
				sequence := line[index : index+length]
				flush()
				if strings.HasSuffix(sequence, "m") {
					style.apply(sequence)
				}
				index += length
				continue
			}
			index++
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(line[index:])
		if size == 0 {
			break
		}
		index += size
		if runeValue < 32 && runeValue != '\t' {
			continue
		}
		text.WriteRune(runeValue)
	}
	flush()
	return output.String()
}

func escapeHTML(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		switch character {
		case '&':
			escaped.WriteString("&amp;")
		case '<':
			escaped.WriteString("&lt;")
		case '>':
			escaped.WriteString("&gt;")
		case '"':
			escaped.WriteString("&quot;")
		case '\'':
			escaped.WriteString("&#39;")
		default:
			escaped.WriteRune(character)
		}
	}
	return escaped.String()
}

var standardANSIColors = [...]string{
	"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0",
	"#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff",
}

func ansi16Color(index int) string {
	if index < 0 || index >= len(standardANSIColors) {
		return ""
	}
	return standardANSIColors[index]
}

func ansi256Color(index int) string {
	if index < 0 {
		index = 0
	}
	if index > 255 {
		index = 255
	}
	if index < 16 {
		return ansi16Color(index)
	}
	if index < 232 {
		value := index - 16
		red, green, blue := value/36, (value/6)%6, value%6
		component := func(value int) int {
			if value == 0 {
				return 0
			}
			return 55 + value*40
		}
		return fmt.Sprintf("#%02x%02x%02x", component(red), component(green), component(blue))
	}
	gray := 8 + (index-232)*10
	return fmt.Sprintf("#%02x%02x%02x", gray, gray, gray)
}

func clampByte(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}
