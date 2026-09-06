// Package learning stores reviewed, globally reusable knowledge for one OS user.
package learning

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	Version          = 1
	DefaultBudget    = 1200
	MaxRecordBytes   = 16 * 1024
	MaxRecords       = 512
	MaxProposalBytes = 64 * 1024
)

var ErrConflict = errors.New("global learning changed since review; run /learn again to review a fresh proposal")

type Learning struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Topic     string    `json:"topic"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source"`
}

type Snapshot struct {
	Items    []Learning
	Revision string
}

// Store applies a reviewed batch against its exact source revision. A single
// CAS boundary covers additions, updates, deletion, and compaction alike.
type Store interface {
	Snapshot(context.Context) (Snapshot, error)
	Search(context.Context, string, int) ([]Learning, error)
	Apply(context.Context, Plan) (string, error)
}

// Approver is supplied only by the interactive host.
type Approver func(context.Context, []Change) (bool, error)

type Change struct{ Before, After *Learning }
type Plan struct {
	Revision string
	Changes  []Change
	Compact  bool
}

type Draft struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	Topic   string   `json:"topic"`
	Content string   `json:"content"`
	Tags    []string `json:"tags"`
}

// FileStore owns <learning-dir>/v1/global. New does not create files.
type FileStore struct {
	root   string
	warn   func(string)
	rename func(string, string) error
}

func New(root string, warn func(string)) *FileStore {
	return &FileStore{root: filepath.Join(root, "v1"), warn: warn, rename: os.Rename}
}

func DefaultDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return StateDirectory(runtime.GOOS, home, os.Getenv("XDG_STATE_HOME"), os.Getenv("LocalAppData"))
}
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
		return "", fmt.Errorf("cannot locate absolute learning state directory")
	}
	return filepath.Join(root, "qcode", "learning"), nil
}
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Reject common credential forms in addition to the extraction prompt and user
// review. This deliberately does not claim to identify every possible secret.
var secretPattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:sk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|AKIA[A-Z0-9]{16})\b|(?:api[_ -]?key|password|passwd|secret|access[_ -]?token)\s*[:=]\s*["']?[^\s"'<]{8,})`)

func ContainsSecret(s string) bool { return secretPattern.MatchString(s) }
func Redact(s string) string       { return secretPattern.ReplaceAllString(s, "[REDACTED]") }
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
func validate(l Learning) error {
	if l.Version != Version {
		return fmt.Errorf("unsupported learning version %d", l.Version)
	}
	if !validID.MatchString(l.ID) || !validID.MatchString(l.Source) {
		return fmt.Errorf("invalid learning or source ID")
	}
	if !validText(l.Topic, 160, false) || !validText(l.Content, 4096, true) || len(l.Tags) > 16 {
		return fmt.Errorf("invalid learning text or tags")
	}
	for _, tag := range l.Tags {
		if !validText(tag, 64, false) {
			return fmt.Errorf("invalid learning tag")
		}
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.Before(l.CreatedAt) {
		return fmt.Errorf("invalid learning timestamps")
	}
	if ContainsSecret(l.Topic + "\n" + l.Content + "\n" + strings.Join(l.Tags, " ")) {
		return fmt.Errorf("learning contains a possible credential; remove it before saving")
	}
	return nil
}
func Decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func NewPlan(snapshot Snapshot, drafts []Draft, source string, compact bool) (Plan, error) {
	p := Plan{Revision: snapshot.Revision, Compact: compact}
	if len(drafts) > 32 {
		return p, fmt.Errorf("at most 32 proposed changes are allowed")
	}
	existing := map[string]Learning{}
	for _, item := range snapshot.Items {
		existing[item.ID] = item
	}
	seen := map[string]bool{}
	now := time.Now().UTC()
	for _, d := range drafts {
		var before *Learning
		switch d.Kind {
		case "add":
			if d.ID != "" {
				return p, fmt.Errorf("new learning must not specify an ID")
			}
			id, err := NewID()
			if err != nil {
				return p, err
			}
			d.ID = id
		case "update", "delete":
			old, ok := existing[d.ID]
			if !ok {
				return p, fmt.Errorf("unknown learning ID %q", d.ID)
			}
			before = &old
			if d.Kind == "delete" && !compact {
				return p, fmt.Errorf("extraction cannot delete learning")
			}
		default:
			return p, fmt.Errorf("unknown change kind %q", d.Kind)
		}
		if seen[d.ID] {
			return p, fmt.Errorf("multiple changes for one learning ID")
		}
		seen[d.ID] = true
		if d.Kind == "delete" {
			if d.Topic != "" || d.Content != "" || len(d.Tags) > 0 {
				return p, fmt.Errorf("delete must contain only kind and ID")
			}
			p.Changes = append(p.Changes, Change{Before: before})
			continue
		}
		after := Learning{Version: Version, ID: d.ID, Topic: d.Topic, Content: d.Content, Tags: d.Tags, CreatedAt: now, UpdatedAt: now, Source: source}
		if before != nil {
			after.CreatedAt = before.CreatedAt
		}
		if err := validate(after); err != nil {
			return p, err
		}
		p.Changes = append(p.Changes, Change{Before: before, After: &after})
	}
	return p, nil
}

func (s *FileStore) Snapshot(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	err := s.locked(ctx, false, func() error { var err error; snapshot, err = s.snapshot(ctx); return err })
	return snapshot, err
}
func (s *FileStore) snapshot(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	dir := filepath.Join(s.root, "global")
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snapshot, err
	}
	if len(entries) > MaxRecords {
		return snapshot, fmt.Errorf("learning store exceeds %d entries", MaxRecords)
	}
	hash := sha256.New()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			return snapshot, err
		}
		if !info.Mode().IsRegular() || info.Size() > MaxRecordBytes {
			fmt.Fprintf(hash, "invalid:%q:%v:%d:%d;", name, info.Mode(), info.Size(), info.ModTime().UnixNano())
			if s.warn != nil {
				s.warn(fmt.Sprintf("skipping invalid learning %q: non-regular or oversized entry", name))
			}
			continue
		}
		// Hash every byte, including invalid records, to detect concurrent changes.
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return snapshot, err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, MaxRecordBytes+1))
		f.Close()
		if readErr != nil {
			return snapshot, readErr
		}
		if len(data) > MaxRecordBytes {
			return snapshot, fmt.Errorf("learning entry %q exceeds size limit", name)
		}
		fmt.Fprintf(hash, "%d:%s:%d:", len(name), name, len(data))
		hash.Write(data)
		var item Learning
		err = Decode(data, &item)
		if err == nil {
			err = validate(item)
		}
		if err == nil && name != item.ID+".json" {
			err = fmt.Errorf("filename does not match learning ID")
		}
		if err != nil {
			if s.warn != nil {
				s.warn(fmt.Sprintf("skipping invalid learning %q: %v", name, err))
			}
			continue
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	snapshot.Revision = hex.EncodeToString(hash.Sum(nil))
	sort.Slice(snapshot.Items, func(i, j int) bool { return snapshot.Items[i].ID < snapshot.Items[j].ID })
	return snapshot, nil
}
