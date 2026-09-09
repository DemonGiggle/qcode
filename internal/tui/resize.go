package tui

import "fmt"

func (u *UI) watchResize() func() {
	events, stop := terminalResizeEvents()
	if events == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-events:
				u.resize()
			case <-done:
				return
			}
		}
	}()
	return func() {
		close(done)
		stop()
	}
}

func (u *UI) resize() {
	width, height := terminalSize(u.out)
	u.screenMu.Lock()
	if width == u.width && height == u.height {
		u.screenMu.Unlock()
		return
	}
	u.width, u.height = width, height
	// Terminal reflow can change rows even when their source text is the same.
	// Force the next fixed-layout render to rebuild the full screen.
	u.inputFrame = ""
	views := make([]*agentView, 0, len(u.views))
	for _, view := range u.views {
		views = append(views, view)
	}
	u.screenMu.Unlock()
	for _, view := range views {
		view.response.SetWidth(width)
	}
	if len(views) == 0 {
		u.responseWriter.SetWidth(width)
	}
	u.commandMenu.setWidth(width)
	u.screenMu.Lock()
	u.terminal.SetSize(width, height)
	if height < 4 {
		u.statusActive = false
		fmt.Fprint(u.out, "\x1b[r\x1b[H\x1b[J")
		u.screenMu.Unlock()
		return
	}
	u.statusActive = true
	fmt.Fprintf(u.out, "\x1b[2;%dr\x1b[2;1H", height-2)
	u.drawTabBarLocked()
	u.drawStatusBarLocked()
	u.screenMu.Unlock()
	u.repaintActive()
}
