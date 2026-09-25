package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
)

// Login credentials never pass through display/history: those are exported,
// persisted, and mirrored to remote controllers.
func (u *UI) showRemoteLogin(login RemoteLogin) {
	qr, err := qrcode.New(login.URL, qrcode.Medium)
	var rows []string
	if err == nil {
		rows = terminalQR(qr.Bitmap())
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.remoteLoginTimer != nil {
		u.remoteLoginTimer.Stop()
	}
	u.removeRemoteQRFileLocked()
	u.remoteLogin, u.remoteQR = &login, rows
	if login.OpenAccess {
		if u.fixedInput {
			u.paintFixedLocked(0)
		}
		return
	}
	u.remoteLoginTimer = time.AfterFunc(time.Until(login.ExpiresAt), func() {
		u.screenMu.Lock()
		defer u.screenMu.Unlock()
		if u.remoteLogin != &login {
			return
		}
		// Drop the raw token as soon as its display expires.
		u.remoteLogin.URL, u.remoteQR = "", nil
		u.removeRemoteQRFileLocked()
		if u.fixedInput {
			u.paintFixedLocked(0)
		} else if u.out != nil {
			fmt.Fprintln(u.out, "\r\nLogin link expired. Run /remote for a new link.\r")
		}
	})
	if u.fixedInput {
		u.paintFixedLocked(0)
	} else if u.out != nil {
		for i, row := range u.remoteLoginRows(max(1, u.width), max(1, u.height-4)) {
			if i == 0 {
				row = u.remoteLoginHeading(row)
			}
			fmt.Fprintf(u.out, "%s\r\n", row)
		}
	}
}

func (u *UI) clearRemoteLogin() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.remoteLoginTimer != nil {
		u.remoteLoginTimer.Stop()
		u.remoteLoginTimer = nil
	}
	u.removeRemoteQRFileLocked()
	if u.remoteLogin == nil {
		return
	}
	u.remoteLogin, u.remoteQR = nil, nil
	if u.fixedInput {
		u.paintFixedLocked(0)
	}
}

// saveRemoteQR stores only the current login's QR in a private temporary file.
// The file is removed with the login rather than remaining as a stale token.
func (u *UI) saveRemoteQR() (string, error) {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.remoteLogin == nil || u.remoteLogin.URL == "" || (!u.remoteLogin.OpenAccess && !time.Now().Before(u.remoteLogin.ExpiresAt)) {
		return "", errors.New("login link expired; run /remote for a new link")
	}
	if u.remoteQRFile != "" {
		return u.remoteQRFile, nil
	}
	image, err := qrcode.Encode(u.remoteLogin.URL, qrcode.Medium, 512)
	if err != nil {
		return "", fmt.Errorf("generate QR PNG: %w", err)
	}
	file, err := os.CreateTemp("", "qcode-remote-*.png")
	if err != nil {
		return "", fmt.Errorf("create QR PNG: %w", err)
	}
	path := file.Name()
	if _, err = file.Write(image); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write QR PNG: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close QR PNG: %w", err)
	}
	u.remoteQRFile = path
	return path, nil
}

// Caller holds screenMu.
func (u *UI) removeRemoteQRFileLocked() {
	if u.remoteQRFile != "" {
		_ = os.Remove(u.remoteQRFile)
		u.remoteQRFile = ""
	}
}

// Caller holds screenMu. The QR is either displayed intact or omitted.
func (u *UI) remoteLoginRows(width, height int) []string {
	if u.remoteLogin == nil || width < 1 || height < 1 {
		return nil
	}
	login := u.remoteLogin
	if login.OpenAccess {
		rows := []string{"Remote link (NO AUTH — anyone with this URL can control qcode)"}
		for remaining := login.URL; len(remaining) > 0; {
			n := min(len(remaining), width)
			rows = append(rows, remaining[:n])
			remaining = remaining[n:]
		}
		rows = append(rows, "")
		if len(u.remoteQR) > 0 && visibleWidth(u.remoteQR[0]) <= width && len(rows)+len(u.remoteQR) <= height {
			rows = append(rows, u.remoteQR...)
		} else {
			rows = append(rows, "Enlarge the terminal or select Save QR as PNG.")
		}
		if len(rows) > height {
			return []string{truncateDiffLine("Enlarge the terminal or select Save QR as PNG.", width, false)}
		}
		for i := range rows {
			rows[i] = truncateDiffLine(rows[i], width, true)
		}
		return rows
	}
	if !time.Now().Before(login.ExpiresAt) {
		return []string{truncateDiffLine("Login link expired. Run /remote for a new link.", width, false)}
	}
	rows := []string{"Remote login (click or scan; single use)", "Valid for 3 minutes; expires at " + login.ExpiresAt.Format("15:04:05")}
	// The unmodified URL remains one logical line when it fits; otherwise
	// wrap it by terminal cells without passing it through shared history.
	for remaining := login.URL; len(remaining) > 0; {
		n := min(len(remaining), width)
		rows = append(rows, remaining[:n])
		remaining = remaining[n:]
	}
	rows = append(rows, "")
	if len(u.remoteQR) > 0 && visibleWidth(u.remoteQR[0]) <= width && len(rows)+len(u.remoteQR) <= height {
		rows = append(rows, u.remoteQR...)
	} else {
		rows = append(rows, "Enlarge the terminal or select Save QR as PNG.")
	}
	if len(rows) > height {
		return []string{truncateDiffLine("Enlarge the terminal or select Save QR as PNG.", width, false)}
	}
	for i := range rows {
		rows[i] = truncateDiffLine(rows[i], width, true)
	}
	return rows
}

// OSC 8 keeps the complete target clickable even when the displayed URL wraps.
// Add it after layout so the secret target never affects measured row widths.
func (u *UI) remoteLoginHeading(row string) string {
	if u.remoteLogin == nil || u.remoteLogin.URL == "" || (!u.remoteLogin.OpenAccess && !time.Now().Before(u.remoteLogin.ExpiresAt)) {
		return row
	}
	return terminalLink(row, u.remoteLogin.URL)
}

func terminalLink(label, target string) string {
	return "\x1b]8;;" + target + "\x1b\\" + label + "\x1b]8;;\x1b\\"
}

func terminalQR(bitmap [][]bool) []string {
	var rows []string
	for y := 0; y < len(bitmap); y += 2 {
		var row strings.Builder
		row.WriteString("\x1b[30;47m")
		for x, top := range bitmap[y] {
			bottom := y+1 < len(bitmap) && bitmap[y+1][x]
			switch {
			case top && bottom:
				row.WriteRune('█')
			case top:
				row.WriteRune('▀')
			case bottom:
				row.WriteRune('▄')
			default:
				row.WriteByte(' ')
			}
		}
		row.WriteString("\x1b[0m")
		rows = append(rows, row.String())
	}
	return rows
}
