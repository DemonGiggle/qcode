package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
)

const planViewHint = "Plan view | PgUp/PgDn or Up/Down to scroll | q/Esc/Ctrl+C to close"

type planPager struct {
	lines []string
	top   int
}

func newPlanPager(plan string, width int) planPager {
	plan = strings.ReplaceAll(plan, "\r\n", "\n")
	plan = strings.ReplaceAll(plan, "\r", "\n")
	wrapped := wrapANSI(plan, max(1, width), "")
	return planPager{lines: strings.Split(wrapped, "\n")}
}

func (p planPager) maxTop(visible int) int {
	return max(0, len(p.lines)-max(1, visible))
}

func (p *planPager) move(delta, visible int) {
	p.top = max(0, min(p.maxTop(visible), p.top+delta))
}

func (p *planPager) page(direction, visible int) {
	p.move(direction*max(1, visible), visible)
}

func (p *planPager) home() {
	p.top = 0
}

func (p *planPager) end(visible int) {
	p.top = p.maxTop(visible)
}

// showPlanPager renders a plan without touching the history writer. The caller
// owns the terminal's raw-mode and screen-region setup.
func showPlanPager(in io.Reader, out io.Writer, plan string, width, height int, color bool) error {
	height = max(1, height)
	pager := newPlanPager(plan, width)
	render := func() {
		renderPlanPager(out, pager, width, height, color)
	}
	render()

	for {
		key, err := readSelectorKey(in)
		if err != nil {
			return err
		}
		switch key {
		case string([]byte{ctrlC}), "q", "Q", "\x1b":
			return nil
		case selectorPageUp:
			pager.page(-1, height-1)
			render()
		case selectorPageDown:
			pager.page(1, height-1)
			render()
		case arrowUpSequence:
			pager.move(-1, height-1)
			render()
		case arrowDownSequence:
			pager.move(1, height-1)
			render()
		case "\x1b[H", "g":
			pager.home()
			render()
		case "\x1b[F", "G":
			pager.end(height - 1)
			render()
		}
	}
}

func renderPlanPager(out io.Writer, pager planPager, width, height int, color bool) {
	if width < 1 {
		width = 1
	}
	header := planViewHint
	if pager.maxTop(height-1) > 0 {
		first := pager.top + 1
		last := min(len(pager.lines), pager.top+max(1, height-1))
		header = fmt.Sprintf("%s (%d-%d/%d)", header, first, last, len(pager.lines))
	}
	if color {
		header = dim + header + reset
	}
	var output strings.Builder
	for row := 0; row < height; row++ {
		line := ""
		if row == 0 {
			line = header
		} else {
			index := pager.top + row - 1
			if index < len(pager.lines) {
				line = pager.lines[index]
			}
		}
		fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K%s\x1b[0m", row+2, truncateDiffLine(line, width, false))
	}
	_, _ = io.WriteString(out, output.String())
}

type planViewWriter struct{ ui *UI }

func (w planViewWriter) Write(data []byte) (int, error) {
	w.ui.screenMu.Lock()
	defer w.ui.screenMu.Unlock()
	return w.ui.out.Write(data)
}

func (u *UI) showPlanView(ctx context.Context, plan string) error {
	if u.input == nil || u.out == nil || !u.fixedInput {
		return fmt.Errorf("plan view requires an interactive terminal")
	}

	u.screenMu.Lock()
	u.planViewActive = true
	u.screenMu.Unlock()
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() {
		u.input.setRaw(false)
		u.screenMu.Lock()
		u.planViewActive = false
		u.screenMu.Unlock()
		u.endRawSelector()
	}()

	u.screenMu.Lock()
	width, height := u.width, u.height
	u.screenMu.Unlock()
	visible := max(1, height-4)
	return showPlanPager(u.input, planViewWriter{ui: u}, plan, width, visible, ColorEnabled(u.out))
}
