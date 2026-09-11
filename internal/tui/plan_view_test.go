package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPlanPagerScrollsByPage(t *testing.T) {
	pager := newPlanPager("one\ntwo\nthree\nfour\nfive", 80)
	if pager.top != 0 || pager.maxTop(2) != 3 {
		t.Fatalf("initial pager = %+v, max top = %d", pager, pager.maxTop(2))
	}

	pager.page(1, 2)
	if pager.top != 2 {
		t.Fatalf("page down top = %d, want 2", pager.top)
	}
	pager.page(1, 2)
	if pager.top != 3 {
		t.Fatalf("page down at end top = %d, want 3", pager.top)
	}
	pager.home()
	if pager.top != 0 {
		t.Fatalf("home top = %d, want 0", pager.top)
	}
	pager.end(2)
	if pager.top != 3 {
		t.Fatalf("end top = %d, want 3", pager.top)
	}
}

func TestShowPlanPagerScrollsAndCloses(t *testing.T) {
	var output bytes.Buffer
	plan := strings.Join([]string{"one", "two", "three", "four", "five", "six"}, "\n")
	input := strings.NewReader(selectorPageDown + "q")

	if err := showPlanPager(input, &output, plan, 80, 4, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "four") || !strings.Contains(output.String(), "six") {
		t.Fatalf("scrolled plan output = %q", output.String())
	}
}

type planViewTestRunner struct {
	plan string
}

func (*planViewTestRunner) Run(context.Context, string) error { return nil }
func (*planViewTestRunner) PlanMode() bool                    { return true }
func (*planViewTestRunner) SetPlanMode(bool)                  {}
func (r *planViewTestRunner) LatestPlanText() (string, bool)  { return r.plan, r.plan != "" }
func (*planViewTestRunner) ClearLatestPlan()                  {}

func TestPlanShowDoesNotAddPlanToHistory(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "plan-view")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	input := newInterruptReader(nil)
	input.data <- 'q'
	display := newHistoryWriter(io.Discard)
	display.AddLine("existing output")
	u := &UI{
		fixedInput: true,
		input:      input,
		out:        out,
		width:      80,
		height:     12,
		display:    display,
		runner:     &planViewTestRunner{plan: "latest plan text"},
	}

	u.handlePlanCommand(context.Background(), []string{"/plan", "show"})
	if got := strings.Join(display.Lines(), "\n"); strings.Contains(got, "latest plan text") {
		t.Fatalf("plan leaked into history: %q", got)
	}
	if !strings.Contains(strings.Join(display.Lines(), "\n"), "existing output") {
		t.Fatalf("existing history was lost: %q", display.Lines())
	}
}
