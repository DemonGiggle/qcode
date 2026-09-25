package tui

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skip2/go-qrcode"
)

func TestRemoteLoginIsTerminalOnly(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := New(nil, out, nil, "test", "model", ".")
	u.fixedInput, u.width, u.height = true, 160, 65
	login := RemoteLogin{URL: "https://host.tailnet.ts.net/qcode/ab#login=private-login-token", ExpiresAt: time.Now().Add(time.Minute)}
	u.showRemoteLogin(login)
	defer u.clearRemoteLogin()
	written, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), login.URL) || !strings.Contains(string(written), "\x1b[30;47m") {
		t.Fatal("terminal did not show link and QR")
	}
	remote, _ := json.Marshal(u.RemotePresentation())
	persisted, _ := json.Marshal(u.snapshotPresentation())
	exported := renderExportTranscript(u.display.ExportSnapshot())
	for name, content := range map[string]string{"remote": string(remote), "saved": string(persisted), "export": exported} {
		if strings.Contains(content, "private-login-token") || strings.Contains(content, "30;47") {
			t.Fatalf("%s contains login credentials", name)
		}
	}
	u.clearRemoteLogin()
	if u.remoteLogin != nil || u.remoteLoginTimer != nil || len(u.remoteQR) != 0 {
		t.Fatal("login display was not cleared")
	}
}

func TestRemoteLoginPanelExpiryAndSizing(t *testing.T) {
	login := RemoteLogin{URL: "https://host.tailnet.ts.net/qcode/ab#login=secret", ExpiresAt: time.Now().Add(time.Minute)}
	qr, err := qrcode.New(login.URL, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	u := &UI{remoteLogin: &login, remoteQR: terminalQR(qr.Bitmap())}
	for _, size := range []struct{ width, height int }{{80, 45}, {40, 24}, {10, 3}} {
		rows := u.remoteLoginRows(size.width, size.height)
		if len(rows) > size.height {
			t.Fatal("panel overflows terminal height")
		}
		qrRows := 0
		for _, row := range rows {
			if visibleWidth(row) > size.width {
				t.Fatal("panel overflows terminal width")
			}
			if strings.Contains(row, "\x1b[30;47m") {
				qrRows++
			}
		}
		if qrRows != 0 && qrRows != len(u.remoteQR) {
			t.Fatal("QR code was cropped")
		}
	}
	login.ExpiresAt = time.Now().Add(-time.Second)
	rows := strings.Join(u.remoteLoginRows(80, 24), "\n")
	if !strings.Contains(rows, "expired") || strings.Contains(rows, "secret") {
		t.Fatal("expired panel did not hide credentials")
	}
}

func TestRemoteLoginExpiryTimerDoesNotClearReplacement(t *testing.T) {
	u := New(nil, nil, nil, "test", "model", ".")
	u.showRemoteLogin(RemoteLogin{URL: "https://host/#login=old", ExpiresAt: time.Now().Add(10 * time.Millisecond)})
	u.showRemoteLogin(RemoteLogin{URL: "https://host/#login=new", ExpiresAt: time.Now().Add(time.Minute)})
	defer u.clearRemoteLogin()
	time.Sleep(25 * time.Millisecond)
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.remoteLogin == nil || !strings.Contains(u.remoteLogin.URL, "new") {
		t.Fatal("old expiry cleared replacement")
	}
}

func TestSaveRemoteQRCreatesPrivatePNGAndRemovesItWithLogin(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	u := New(nil, nil, nil, "test", "model", ".")
	u.showRemoteLogin(RemoteLogin{URL: "https://host/#login=secret", ExpiresAt: time.Now().Add(time.Minute)})
	path, err := u.saveRemoteQR()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("QR PNG permissions = %o, want 600", info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(file)
	file.Close()
	if err != nil || config.Width != 512 || config.Height != 512 {
		t.Fatalf("QR PNG = %dx%d, error = %v", config.Width, config.Height, err)
	}
	again, err := u.saveRemoteQR()
	if err != nil || again != path {
		t.Fatalf("repeated save = %q, %v", again, err)
	}
	u.showRemoteLogin(RemoteLogin{URL: "https://host/#login=replacement", ExpiresAt: time.Now().Add(time.Minute)})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("replaced login left QR PNG: %v", err)
	}
	newPath, err := u.saveRemoteQR()
	if err != nil {
		t.Fatal(err)
	}
	u.clearRemoteLogin()
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("cleared login left QR PNG: %v", err)
	}
}

func TestSavedRemoteQRIsRemovedAtExpiry(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	u := New(nil, nil, nil, "test", "model", ".")
	u.showRemoteLogin(RemoteLogin{URL: "https://host/#login=secret", ExpiresAt: time.Now().Add(250 * time.Millisecond)})
	defer u.clearRemoteLogin()
	path, err := u.saveRemoteQR()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expired login left QR PNG")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := u.saveRemoteQR(); err == nil {
		t.Fatal("expired login was saved again")
	}
}

func TestOpenRemoteLinkHasNoExpiry(t *testing.T) {
	login := RemoteLogin{URL: "http://192.168.1.10:1234", OpenAccess: true}
	qr, err := qrcode.New(login.URL, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	u := &UI{remoteLogin: &login, remoteQR: terminalQR(qr.Bitmap())}
	rows := strings.Join(u.remoteLoginRows(80, 40), "\n")
	if !strings.Contains(rows, "NO AUTH") || !strings.Contains(rows, login.URL) || strings.Contains(rows, "expired") {
		t.Fatalf("open remote link rows = %q", rows)
	}
}

// Use an independent scanner when available; Go-only installations still run
// the layout, expiry, and credential-isolation tests above.
func TestTerminalQRDecodesLoginURL(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("QR decode check needs Python and OpenCV")
	}
	if err := exec.Command(python, "-c", "import cv2").Run(); err != nil {
		t.Skip("QR decode check needs OpenCV")
	}
	want := "https://host.tailnet.ts.net/qcode/ab#login=" + strings.Repeat("a", 43)
	qr, err := qrcode.New(want, qrcode.Medium)
	if err != nil {
		t.Fatal(err)
	}
	rows := terminalQR(qr.Bitmap())
	width := visibleWidth(rows[0])
	const scale = 8
	img := image.NewGray(image.Rect(0, 0, width*scale, len(rows)*2*scale))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y, row := range rows {
		for x, char := range []rune(plainHistoryText(row)) {
			for dy := 0; dy < 2; dy++ {
				dark := char == '█' || (dy == 0 && char == '▀') || (dy == 1 && char == '▄')
				if !dark {
					continue
				}
				for py := 0; py < scale; py++ {
					for px := 0; px < scale; px++ {
						img.SetGray(x*scale+px, (y*2+dy)*scale+py, color.Gray{Y: 0})
					}
				}
			}
		}
	}
	file := filepath.Join(t.TempDir(), "login-qr.png")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	output, err := exec.Command(python, "-c", "import cv2,sys; print(cv2.QRCodeDetector().detectAndDecode(cv2.imread(sys.argv[1]))[0])", file).CombinedOutput()
	if err != nil {
		t.Fatalf("decode QR: %v %s", err, output)
	}
	if strings.TrimSpace(string(output)) != want {
		t.Fatalf("decoded QR = %q", output)
	}
}
