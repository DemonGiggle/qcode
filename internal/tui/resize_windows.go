//go:build windows

package tui

import "os"

func terminalResizeEvents() (<-chan os.Signal, func()) { return nil, func() {} }
