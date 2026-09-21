package tui

import (
	"fmt"
	"strings"
)

const remoteMenuWake = "\x00"

type remoteMenuChoice int

const (
	remoteKeepOpen remoteMenuChoice = iota
	remoteCloseConnection
)

type remoteCloseResult int

const (
	remoteCloseKeepOpen remoteCloseResult = iota
	remoteCloseConfirmed
	remoteCloseBack
	remoteCloseCancelled
)

func (u *UI) selectRemoteMode(current RemoteMode) (RemoteMode, bool, error) {
	modes := []RemoteMode{RemoteModePureWeb, RemoteModePureWebOpen, RemoteModeTailscale}
	selected := 0
	for index, mode := range modes {
		if mode == current {
			selected = index
			break
		}
	}
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() { u.input.setRaw(false); u.endRawSelector() }()
	for {
		rows := renderRemoteModeMenu(u.terminal, modes, selected, u.width, ColorEnabled(u.out))
		key, err := readSelectorKey(u.input)
		if err != nil {
			clearSelector(u.terminal, rows)
			return "", false, err
		}
		switch key {
		case "\r", "\n":
			clearSelector(u.terminal, rows)
			return modes[selected], true, nil
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(u.terminal, rows)
			return "", false, nil
		case arrowUpSequence, arrowDownSequence:
			if key == arrowUpSequence {
				selected = (selected - 1 + len(modes)) % len(modes)
			} else {
				selected = (selected + 1) % len(modes)
			}
			clearSelector(u.terminal, rows)
		}
	}
}

func renderRemoteModeMenu(out interface{ Write([]byte) (int, error) }, modes []RemoteMode, selected, width int, color bool) int {
	rows := []string{selectorHeader("Remote control | Choose how to connect | Up/Down, Enter", width)}
	for index, mode := range modes {
		name, detail := "Pure Web", "Trusted LAN HTTP; scan a one-time QR link"
		if mode == RemoteModePureWebOpen {
			name, detail = "Pure Web (No auth, danger!)", "Trusted LAN HTTP; anyone who knows the URL can control qcode"
		} else if mode == RemoteModeTailscale {
			name, detail = "Tailscale", "HTTPS through tailscale serve and named tailnet identity"
		}
		prefix := "  "
		if index == selected {
			prefix = "> "
		}
		line := prefix + name + " — " + detail
		if color && index == selected {
			line = cyan + bold + line + reset
		}
		rows = append(rows, line)
	}
	for _, row := range rows {
		fmt.Fprintln(out, truncateDiffLine(row, width, false))
	}
	return len(rows)
}

func (u *UI) selectRemoteNetwork(networks []RemoteNetwork) (bool, RemoteNetwork, bool, error) {
	selected := 0
	u.input.setRaw(true)
	u.beginRawSelector()
	defer func() { u.input.setRaw(false); u.endRawSelector() }()
	for {
		rows := renderRemoteNetworkMenu(u.terminal, networks, selected, u.width, ColorEnabled(u.out))
		key, err := readSelectorKey(u.input)
		if err != nil {
			clearSelector(u.terminal, rows)
			return false, RemoteNetwork{}, false, err
		}
		switch key {
		case "\r", "\n":
			clearSelector(u.terminal, rows)
			return true, networks[selected], false, nil
		case string([]byte{ctrlC}):
			clearSelector(u.terminal, rows)
			return false, RemoteNetwork{}, false, nil
		case "\x1b":
			clearSelector(u.terminal, rows)
			return false, RemoteNetwork{}, true, nil
		case arrowUpSequence, arrowDownSequence:
			if key == arrowUpSequence {
				selected = (selected - 1 + len(networks)) % len(networks)
			} else {
				selected = (selected + 1) % len(networks)
			}
			clearSelector(u.terminal, rows)
		}
	}
}

func renderRemoteNetworkMenu(out interface{ Write([]byte) (int, error) }, networks []RemoteNetwork, selected, width int, color bool) int {
	rows := []string{selectorHeader("Pure Web | Choose LAN interface | Up/Down, Enter", width)}
	for index, network := range networks {
		prefix := "  "
		if index == selected {
			prefix = "> "
		}
		line := fmt.Sprintf("%s%s — %s (subnet %s)", prefix, network.Name, network.Address, network.Subnet)
		if color && index == selected {
			line = cyan + bold + line + reset
		}
		rows = append(rows, line)
	}
	for _, row := range rows {
		fmt.Fprintln(out, truncateDiffLine(row, width, false))
	}
	return len(rows)
}

func (u *UI) showRemoteActive(status RemoteStatus) error {
	selected := remoteKeepOpen
	staticRows := 6 // title, address, connections, spacer, and two actions
	if status.Mode != RemoteModeTailscale {
		staticRows++
	}
	loginHeight := max(1, u.height-3-staticRows)
	u.armRemoteMenu()
	u.beginRawSelector()
	defer func() {
		u.setRemoteMenuActive(false)
		u.input.setRaw(false)
		u.endRawSelector()
	}()
	for {
		u.screenMu.Lock()
		loginRows := u.remoteLoginRows(u.width, loginHeight)
		loginURL := ""
		if u.remoteLogin != nil {
			loginURL = u.remoteLogin.URL
		}
		u.screenMu.Unlock()
		rows := renderRemoteActiveMenu(u.terminal, status, selected, loginRows, loginURL, u.width, ColorEnabled(u.out))
		key, err := readSelectorKey(u.input)
		if err != nil {
			clearSelector(u.terminal, rows)
			return err
		}
		switch key {
		case remoteMenuWake:
			clearSelector(u.terminal, rows)
			return nil
		case string([]byte{ctrlC}), "\x1b":
			clearSelector(u.terminal, rows)
			return nil
		case arrowUpSequence, arrowDownSequence:
			selected = 1 - selected
			clearSelector(u.terminal, rows)
		case "\r", "\n":
			clearSelector(u.terminal, rows)
			if selected == remoteKeepOpen {
				return nil
			}
			confirmation, err := u.confirmRemoteClose(status)
			if err != nil {
				return err
			}
			switch confirmation {
			case remoteCloseBack:
				continue
			case remoteCloseKeepOpen, remoteCloseCancelled:
				return nil
			}
			u.clearRemoteLogin()
			if err := u.remoteService.Stop(); err != nil {
				return err
			}
			u.drawStatusBar()
			u.printSystemMessage(green + "Remote connection closed." + reset)
			return nil
		}
	}
}

func (u *UI) setRemoteMenuActive(active bool) {
	u.remoteMenuMu.Lock()
	u.remoteMenuActive, u.remoteMenuWake = active, false
	u.remoteMenuMu.Unlock()
}

func (u *UI) armRemoteMenu() {
	u.setRemoteMenuActive(true)
	u.input.setRaw(true)
	u.remoteMenuMu.Lock()
	wake := u.remoteMenuActive && u.remoteMenuWake
	u.remoteMenuMu.Unlock()
	if wake && u.input != nil {
		u.input.wakeRaw()
	}
}

func (u *UI) dismissRemoteMenuForInput() {
	u.remoteMenuMu.Lock()
	active := u.remoteMenuActive && !u.remoteMenuWake
	if active {
		u.remoteMenuWake = true
	}
	u.remoteMenuMu.Unlock()
	if active && u.input != nil {
		u.input.wakeRaw()
	}
}

func renderRemoteActiveMenu(out interface{ Write([]byte) (int, error) }, status RemoteStatus, selected remoteMenuChoice, loginRows []string, loginURL string, width int, color bool) int {
	mode := "Pure Web · trusted LAN HTTP"
	if status.Mode == RemoteModePureWebOpen {
		mode = "Pure Web (NO AUTH) · anyone with the URL can control qcode"
	}
	if status.Mode == RemoteModeTailscale {
		mode = "Tailscale · HTTPS and tailnet identity"
	}
	rows := []string{
		"Remote control active | " + mode,
		"Address: " + status.URL,
		fmt.Sprintf("Connections: %d active browser sessions", status.Connections),
	}
	if status.Mode == RemoteModePureWeb {
		rows = append(rows, "Warning: LAN HTTP is unencrypted; use only on a trusted network.")
	} else if status.Mode == RemoteModePureWebOpen {
		rows = append(rows, "DANGER: no login is required; anyone with this URL has full remote control.")
	}
	loginStart := len(rows)
	rows = append(rows, loginRows...)
	loginEnd := len(rows)
	rows = append(rows, "", "  Keep connection open", "  Close Connection")
	actionStart := len(rows) - 2
	for index := range rows {
		row := rows[index]
		if index == actionStart+int(selected) {
			row = ">" + row[1:]
			if color {
				row = cyan + bold + row + reset
			}
		}
		row = truncateDiffLine(row, width, false)
		if loginURL != "" && index >= loginStart && index < loginEnd {
			loginRow := index - loginStart
			if loginRow == 0 || (row != "" && strings.Contains(loginURL, row)) {
				row = terminalLink(row, loginURL)
			}
		}
		fmt.Fprintln(out, row)
	}
	return len(rows)
}

func (u *UI) confirmRemoteClose(status RemoteStatus) (remoteCloseResult, error) {
	selected := remoteKeepOpen
	for {
		rows := []string{
			fmt.Sprintf("Close remote connection? This disconnects %d browser sessions.", status.Connections),
			"  Keep connection open",
			"  Close Connection",
		}
		for index, row := range rows {
			if index == 1+int(selected) {
				row = ">" + row[1:]
				if ColorEnabled(u.out) {
					row = cyan + bold + row + reset
				}
			}
			fmt.Fprintln(u.terminal, truncateDiffLine(row, u.width, false))
		}
		key, err := readSelectorKey(u.input)
		clearSelector(u.terminal, len(rows))
		if err != nil {
			return remoteCloseCancelled, err
		}
		if key == string([]byte{ctrlC}) {
			return remoteCloseCancelled, nil
		}
		if key == "\x1b" {
			return remoteCloseBack, nil
		}
		switch key {
		case arrowUpSequence, arrowDownSequence:
			selected = 1 - selected
		case "\r", "\n":
			if selected == remoteCloseConnection {
				return remoteCloseConfirmed, nil
			}
			return remoteCloseKeepOpen, nil
		}
	}
}
