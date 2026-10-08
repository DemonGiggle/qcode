package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const SnapshotVersion = 1

type SavedAgent struct {
	Summary      Summary
	State        json.RawMessage
	LatestPrompt string `json:",omitempty"`
}

type Snapshot struct {
	Version                int
	ID, Workspace, Preview string
	Created, Saved, Left   time.Time
	Agents                 []SavedAgent
	NextID                 int
	Presentation           json.RawMessage
	Work                   *WorkHistory `json:",omitempty"`
	// Recency preserves resume ordering across saves and advances on user prompts.
	Recency time.Time `json:",omitempty"`
}

type Entry struct {
	Snapshot
	Metadata
	Current         bool
	Problem         string
	MetadataProblem string
}

type Metadata struct {
	Name   string
	Pinned bool
}

type SnapshotFilter func(Snapshot) (Snapshot, error)

// The filter is installed before the store is used concurrently.
type Store struct {
	dir, workspace string
	filter         SnapshotFilter
	nameFilter     func(string) string
	mu             sync.Mutex
}

func (s *Store) SetSnapshotFilter(filter SnapshotFilter) { s.filter = filter }
func (s *Store) SetDefaultSnapshotFilter(filter SnapshotFilter) {
	if s.filter == nil {
		s.filter = filter
	}
}

// SetDefaultNameFilter installs the persistence text policy before concurrent
// use, independently of conversation validation and rewriting.
func (s *Store) SetDefaultNameFilter(filter func(string) string) {
	if s.nameFilter == nil {
		s.nameFilter = filter
	}
}
func (s *Store) Sanitize(snap Snapshot) (Snapshot, error) {
	if s.filter != nil {
		return s.filter(snap)
	}
	return snap, nil
}

// LoadForResume rewrites sanitized state before exposing it. Concurrent saves
// replace complete snapshots; the last successful atomic replacement wins.
func (s *Store) LoadForResume(id string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := s.Load(id)
	if err != nil {
		return snap, err
	}
	snap, err = s.Sanitize(snap)
	if err != nil {
		return Snapshot{}, err
	}
	if s.filter != nil {
		if err = s.save(snap); err != nil {
			return Snapshot{}, fmt.Errorf("rewrite sanitized session: %w", err)
		}
	}
	return snap, nil
}

func DefaultDirectory() (string, error) {
	if runtime.GOOS == "linux" {
		if p := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(p) {
			return filepath.Join(p, "qcode", "sessions"), nil
		}
		home, err := os.UserHomeDir()
		return filepath.Join(home, ".local", "state", "qcode", "sessions"), err
	}
	p, err := os.UserConfigDir()
	return filepath.Join(p, "qcode", "sessions"), err
}

func Open(directory, workspace string) (*Store, error) {
	p, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(p))
	s := &Store{dir: filepath.Join(directory, hex.EncodeToString(hash[:])), workspace: p}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, err
	}
	return s, nil
}

func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16
}

func (s *Store) New() (Snapshot, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Version: SnapshotVersion, ID: hex.EncodeToString(id[:]), Workspace: s.workspace, Created: time.Now().UTC()}
	return snap, nil
}

func (s *Store) Save(snap Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(snap)
}

func (s *Store) save(snap Snapshot) error {
	if !validID(snap.ID) || snap.Workspace != s.workspace || snap.Version != SnapshotVersion {
		return fmt.Errorf("invalid session snapshot")
	}
	snap, err := s.Sanitize(snap)
	if err != nil {
		return err
	}
	if !validID(snap.ID) || snap.Workspace != s.workspace || snap.Version != SnapshotVersion {
		return fmt.Errorf("invalid filtered session snapshot")
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return atomicWrite(s.dir, snap.ID+".json", data)
}

// atomicWrite creates a private temporary file and replaces only its target.
// Snapshot saves and individual metadata fields never overwrite one another.
func atomicWrite(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".session-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceFile(f.Name(), filepath.Join(dir, name))
}

func (s *Store) Load(id string) (Snapshot, error) {
	var snap Snapshot
	if !validID(id) {
		return snap, fmt.Errorf("invalid session ID")
	}
	data, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, err
	}
	if snap.ID != id || snap.Workspace != s.workspace || snap.Version != SnapshotVersion {
		return snap, fmt.Errorf("incompatible session snapshot %s", id)
	}
	return snap, nil
}

func Departure(s Snapshot) time.Time {
	if !s.Left.IsZero() {
		return s.Left
	}
	return s.Saved
}

// ResumeTime retains the existing order for snapshots saved before Recency was
// introduced. A new session without a prompt starts at its creation time.
func ResumeTime(s Snapshot) time.Time {
	if !s.Recency.IsZero() {
		return s.Recency
	}
	if when := Departure(s); !when.IsZero() {
		return when
	}
	return s.Created
}

func (s *Store) List() ([]Entry, error) {
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".json")
		if !validID(id) {
			continue
		}
		metadata, metadataErr := s.LoadMetadata(id)
		entry := Entry{Metadata: metadata}
		if metadataErr != nil {
			entry.MetadataProblem = metadataErr.Error()
		}
		snap, err := s.Load(id)
		if err != nil {
			info, statErr := file.Info()
			if statErr != nil {
				return nil, statErr
			}
			problem, filterErr := s.Sanitize(Snapshot{ID: id, Workspace: s.workspace, Version: SnapshotVersion, Preview: "Unreadable or incompatible snapshot: " + err.Error()})
			if filterErr != nil {
				return nil, filterErr
			}
			entry.Snapshot = Snapshot{ID: id, Saved: info.ModTime()}
			entry.Problem = problem.Preview
			entries = append(entries, entry)
			continue
		}
		snap, err = s.Sanitize(snap)
		if err != nil {
			return nil, err
		}
		entry.Snapshot = snap
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Pinned != entries[j].Pinned {
			return entries[i].Pinned
		}
		return ResumeTime(entries[i].Snapshot).After(ResumeTime(entries[j].Snapshot))
	})
	return entries, nil
}
