//go:build linux

package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func securePathSupported(root string) bool {
	file, err := secureOpen(root, root, os.O_RDONLY, 0)
	if err == nil {
		file.Close()
	}
	return err == nil
}

func secureOpen(root, path string, flags int, perm os.FileMode) (*os.File, error) {
	dir, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, os.ErrPermission
	}
	fd, err := unix.Openat2(int(dir.Fd()), rel, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Mode:    uint64(perm.Perm()),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func secureMkdirAll(root, path string, perm os.FileMode) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return os.ErrPermission
	}
	if rel == "." {
		return nil
	}
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	fd := int(dir.Fd())
	ownedFD := -1
	defer func() {
		if ownedFD >= 0 {
			_ = unix.Close(ownedFD)
		}
		_ = dir.Close()
	}()
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := unix.Mkdirat(fd, component, uint32(perm.Perm())); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return mkdirErr
			}
			next, openErr = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			return openErr
		}
		if ownedFD >= 0 {
			_ = unix.Close(ownedFD)
		}
		ownedFD = next
		fd = next
	}
	return nil
}
