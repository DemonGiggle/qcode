package tui

import (
	"os"
	"strings"
)

// UnicodeEnabled reports whether the process locale advertises UTF-8. ANSI
// color support is independent. QCODE_ASCII overrides terminals or remote
// consoles that incorrectly claim UTF-8 support.
func UnicodeEnabled() bool {
	if value, exists := os.LookupEnv("QCODE_ASCII"); exists {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			return value == "0" || value == "false" || value == "no" || value == "off"
		}
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	locale := firstLocale("LC_ALL", "LC_CTYPE", "LANG")
	locale = strings.ToLower(locale)
	return strings.Contains(locale, "utf-8") || strings.Contains(locale, "utf8")
}

func firstLocale(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
