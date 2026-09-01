package tui

import "testing"

func TestUnicodeEnabledUsesLocale(t *testing.T) {
	t.Setenv("QCODE_ASCII", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "C.UTF-8")
	if !UnicodeEnabled() {
		t.Fatal("UTF-8 locale was not detected")
	}
	t.Setenv("LC_ALL", "C")
	if UnicodeEnabled() {
		t.Fatal("C locale was detected as UTF-8")
	}
}

func TestUnicodeEnabledHonorsASCIIOverride(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("QCODE_ASCII", "1")
	if UnicodeEnabled() {
		t.Fatal("QCODE_ASCII did not disable Unicode")
	}
	t.Setenv("QCODE_ASCII", "0")
	if !UnicodeEnabled() {
		t.Fatal("QCODE_ASCII=0 did not enable Unicode")
	}
}
