//go:build !linux

package tools

import "os"

func securePathSupported(string) bool { return false }

func secureOpen(_ string, path string, flags int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, perm)
}

func secureMkdirAll(_ string, path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
