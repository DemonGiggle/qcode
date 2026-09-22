package tui

import (
	"bytes"
	_ "embed"
	"html/template"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"

	"qcode/internal/session"
)

//go:embed export_pretty.html
var prettyExportHTML string

var prettyExportTemplate = template.Must(template.New("pretty-export").Parse(prettyExportHTML))

type prettyExchange struct {
	Number                              int
	Prompt, Model, Submitted, Completed string
	Response                            template.HTML
}

type prettyAgent struct {
	ID, Name  string
	Closed    bool
	Active    bool
	Records   []session.WorkRecord
	Exchanges []prettyExchange
}

func prettyExportAgents(summaries []session.Summary, records []session.WorkRecord, active string) []prettyAgent {
	agents := map[string]*prettyAgent{}
	for _, summary := range summaries {
		name := summary.Name
		if name == "" {
			name = summary.ID
		}
		agents[summary.ID] = &prettyAgent{ID: summary.ID, Name: name}
	}
	for _, record := range records {
		if record.Status != "completed" || record.Consultation {
			continue
		}
		agent := agents[record.AgentID]
		if agent == nil {
			agent = &prettyAgent{ID: record.AgentID, Name: record.AgentID, Closed: true}
			agents[record.AgentID] = agent
		}
		if agent.Closed && record.AgentName != "" {
			agent.Name = record.AgentName
		}
		agent.Records = append(agent.Records, record)
	}
	result := make([]prettyAgent, 0, len(agents))
	for _, agent := range agents {
		sort.SliceStable(agent.Records, func(i, j int) bool {
			return agent.Records[i].Created.Before(agent.Records[j].Created)
		})
		agent.Active = agent.ID == active
		result = append(result, *agent)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID == "main" || result[j].ID == "main" {
			return result[i].ID == "main"
		}
		a, aErr := strconv.Atoi(strings.TrimPrefix(result[i].ID, "agent-"))
		b, bErr := strconv.Atoi(strings.TrimPrefix(result[j].ID, "agent-"))
		if aErr == nil && bErr == nil && a != b {
			return a < b
		}
		return result[i].ID < result[j].ID
	})
	if _, ok := agents[active]; !ok && len(result) > 0 {
		result[0].Active = true
	}
	return result
}

func exportTimestamp(value time.Time) string {
	if value.IsZero() {
		return "Time unavailable"
	}
	return value.Local().Format("2006-01-02 15:04:05 -07:00")
}

// exportMarkdown renders native HTML rather than ANSI text. Images become
// links, so opening an offline export never loads external image resources.
func exportMarkdown(markdown string) (template.HTML, error) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(markdown, "\r\n", "\n"), "\r", "\n"), "\n")
	for i := range lines {
		lines[i] = sanitizeDiffLine(lines[i], "<ESC>")
	}
	source := []byte(strings.Join(lines, "\n"))
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	document := md.Parser().Parse(text.NewReader(source))
	var images []*ast.Image
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if image, ok := node.(*ast.Image); ok && entering {
			images = append(images, image)
		}
		return ast.WalkContinue, nil
	})
	for _, image := range images {
		link := ast.NewLink()
		link.Destination, link.Title = image.Destination, image.Title
		link.AppendChild(link, ast.NewString([]byte("Image: ")))
		for image.FirstChild() != nil {
			link.AppendChild(link, image.FirstChild())
		}
		if link.ChildCount() == 1 {
			link.AppendChild(link, ast.NewString(image.Destination))
		}
		image.Parent().ReplaceChild(image.Parent(), image, link)
	}
	var output bytes.Buffer
	if err := md.Renderer().Render(&output, source, document); err != nil {
		return "", err
	}
	// Only Goldmark output with its default safe renderer is trusted here.
	return template.HTML(output.String()), nil
}

func renderPrettyExport(workspace, active string, exportedAt time.Time, summaries []session.Summary, records []session.WorkRecord) ([]byte, error) {
	agents := prettyExportAgents(summaries, records, active)
	total := 0
	for i := range agents {
		for _, record := range agents[i].Records {
			response := record.Response
			if strings.TrimSpace(response) == "" {
				response = "No response text."
			}
			rendered, err := exportMarkdown(response)
			if err != nil {
				return nil, err
			}
			agents[i].Exchanges = append(agents[i].Exchanges, prettyExchange{
				Number: len(agents[i].Exchanges) + 1,
				Prompt: record.Prompt, Model: record.Model, Response: rendered,
				Submitted: exportTimestamp(record.Created), Completed: exportTimestamp(record.Finished),
			})
			total++
		}
	}
	var output bytes.Buffer
	err := prettyExportTemplate.Execute(&output, struct {
		Workspace, Exported string
		Agents              []prettyAgent
		Total               int
	}{workspace, exportTimestamp(exportedAt), agents, total})
	return output.Bytes(), err
}
