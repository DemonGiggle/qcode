// Package memo stores a global user scratchpad for un-sent ideas.
//
// Memos are never injected into model context. The store is global across
// directories and survives restarts, mirroring the learning store layout.
package memo

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Version      = 1
	MaxMemoBytes = 4 * 1024
	MaxMemos     = 200
)

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Memo is one scratchpad entry. Text may contain newlines from the manage
// view add/edit flow; quick-capture is usually a single line.
type Memo struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FileStore owns <memo-dir>/v1/global. New does not create files.
type FileStore struct {
	root string
	warn func(string)
}

func New(root string, warn func(string)) *FileStore {
	return &FileStore{root: filepath.Join(root, "v1"), warn: warn}
}

// DefaultDirectory locates the global memo directory for one OS user.
func DefaultDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return StateDirectory(runtime.GOOS, home, os.Getenv("XDG_STATE_HOME"), os.Getenv("LocalAppData"))
}

// StateDirectory mirrors learning.StateDirectory with a memos leaf.
func StateDirectory(goos, home, xdg, local string) (string, error) {
	var root string
	switch goos {
	case "windows":
		root = local
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support")
	default:
		root = xdg
		if !filepath.IsAbs(root) {
			root = filepath.Join(home, ".local", "state")
		}
	}
	if root == "" || !filepath.IsAbs(root) {
		return "", fmt.Errorf("cannot locate absolute memo state directory")
	}
	return filepath.Join(root, "qcode", "memos"), nil
}

// NewID returns a random 32-hex memo identifier.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func validText(s string, limit int, multiline bool) bool {
	if strings.TrimSpace(s) == "" || len(s) > limit || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}

func validate(m Memo) error {
	if m.Version != Version {
		return fmt.Errorf("unsupported memo version %d", m.Version)
	}
	if !validID.MatchString(m.ID) {
		return fmt.Errorf("invalid memo ID")
	}
	if !validText(m.Text, MaxMemoBytes, true) {
		return fmt.Errorf("invalid memo text")
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.Before(m.CreatedAt) {
		return fmt.Errorf("invalid memo timestamps")
	}
	return nil
}

// Normalize trims surrounding whitespace; empty results are rejected by Add.
func normalize(text string) string {
	return strings.TrimSpace(text)
}
