package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qcode/internal/redaction"
)

// referenceFilterLines replicates the pre-dedupe helper: one Stream per batch
// with no cross-batch cache. Comparing FilterPresentation against it proves
// the per-snapshot line cache is byte-identical.
func referenceFilterLines(p *redaction.Policy, sink redaction.Sink, lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	return strings.Split(p.Text(sink, strings.Join(lines, "\n")), "\n")
}

func referenceFilterPresentation(p *redaction.Policy, data json.RawMessage) (json.RawMessage, error) {
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
		source := h.Archive
		if len(source) == 0 {
			source = h.Lines
		}
		combined := append(append([]string(nil), source...), line.String())
		safe := referenceFilterLines(p, redaction.Persistence, combined)
		filtered := safe[len(safe)-1]
		h.Lines = referenceFilterLines(p, redaction.Persistence, h.Lines)
		h.Archive = referenceFilterLines(p, redaction.Persistence, h.Archive)
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
		v.Diffs = referenceFilterLines(p, redaction.Persistence, v.Diffs)
		var table []string
		for _, line := range v.Table {
			table = append(table, line.Text)
		}
		table = referenceFilterLines(p, redaction.Persistence, table)
		for j := range v.Table {
			v.Table[j].Text = table[j]
		}
	}
	return json.Marshal(s)
}

func repeatedHistoryPresentation(secret string, n int) savedPresentation {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		switch i % 10 {
		case 0:
			lines = append(lines, "payment record Card 4111111111111111 settled")
		case 1:
			lines = append(lines, "credential api_key = "+secret)
		default:
			lines = append(lines, "ordinary log output with no secrets at all 12345 hello world")
		}
	}
	tail := append([]string(nil), lines[len(lines)-200:]...)
	h := savedHistory{Lines: tail, Archive: lines, Cursor: 3}
	for _, r := range "current row with " + secret {
		h.Current = append(h.Current, savedCell{Char: r})
	}
	return savedPresentation{
		Drafts: map[string]string{"main": secret},
		Views: []savedView{{
			ID: "main", Buffer: secret, Diffs: []string{"+" + secret, "+" + secret},
			Table:   []savedTableLine{{Text: secret}, {Text: "plain"}},
			History: h,
		}},
	}
}

func TestFilterPresentationRepeatedHistoryIdentical(t *testing.T) {
	const secret = "synthetic-history-secret"
	p, _ := redaction.New(redaction.Config{}, []string{secret})
	s := repeatedHistoryPresentation(secret, 2000)
	data, _ := json.Marshal(s)

	got, err := FilterPresentation(p, data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := referenceFilterPresentation(p, data)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("cached presentation differs: got %d bytes want %d bytes", len(got), len(want))
	}
	if strings.Contains(string(got), secret) || strings.Contains(string(got), "4111111111111111") {
		t.Fatal("repeated history leaked")
	}
	if !strings.Contains(string(got), redaction.Marker) {
		t.Fatal("expected redaction marker")
	}
	// Idempotent: filtering twice changes nothing.
	again, err := FilterPresentation(p, got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(got) {
		t.Fatal("presentation filtering is not idempotent")
	}
	// Long pathological lines keep their shape and stay marker-free.
	p2, _ := redaction.New(redaction.Config{}, nil)
	patho := savedPresentation{Views: []savedView{{ID: "main", History: savedHistory{
		Lines:   []string{strings.Repeat(" ", 8000), strings.Repeat("\"' * _` \t", 500)},
		Archive: []string{"credit card number" + strings.Repeat(" ", 4000) + "x"},
	}}}}
	pdata, _ := json.Marshal(patho)
	start := time.Now()
	filtered, err := FilterPresentation(p2, pdata)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("pathological history took %v", elapsed)
	}
	var decoded savedPresentation
	if err := json.Unmarshal(filtered, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Views[0].History.Lines) != 2 || len(decoded.Views[0].History.Archive) != 1 {
		t.Fatalf("history shape changed: %+v", decoded.Views[0].History)
	}
}

func TestFilterRepeatedHistoryTiming(t *testing.T) {
	const secret = "synthetic-timing-secret"
	p, _ := redaction.New(redaction.Config{}, []string{secret})
	s := repeatedHistoryPresentation(secret, 3000)
	data, _ := json.Marshal(s)
	start := time.Now()
	got, err := FilterPresentation(p, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("filter 3000-line repeated history: %v (%d bytes in, %d out)", time.Since(start), len(data), len(got))
	if strings.Contains(string(got), secret) {
		t.Fatal("leaked")
	}
}

func BenchmarkFilterRepeatedHistory(b *testing.B) {
	const secret = "synthetic-bench-secret"
	p, _ := redaction.New(redaction.Config{}, []string{secret})
	s := repeatedHistoryPresentation(secret, 2000)
	data, _ := json.Marshal(s)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := FilterPresentation(p, data); err != nil {
			b.Fatal(err)
		}
	}
}
