package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const SnapshotVersion = 1

var ErrSessionBusy = errors.New("session is open in another process")

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
}

type Entry struct {
	Snapshot
	Busy    bool
	Problem string
}

type SnapshotFilter func(Snapshot) (Snapshot, error)

// The filter is installed before the store is used concurrently.
type Store struct {
	dir, workspace string
	filter         SnapshotFilter
}

func (s *Store) SetSnapshotFilter(filter SnapshotFilter) { s.filter = filter }
func (s *Store) SetDefaultSnapshotFilter(filter SnapshotFilter) {
	if s.filter == nil {
		s.filter = filter
	}
}
func (s *Store) Sanitize(snap Snapshot) (Snapshot, error) {
	if s.filter != nil {
		return s.filter(snap)
	}
	return snap, nil
}

// LoadForResume must be called while holding the session lock. Rewrite before
// any restored state is exposed, with no backup containing the original text.
func (s *Store) LoadForResume(id string) (Snapshot, error) {
	snap, err := s.Load(id)
	if err != nil {
		return snap, err
	}
	snap, err = s.Sanitize(snap)
	if err != nil {
		return Snapshot{}, err
	}
	if s.filter != nil {
		if err = s.Save(snap); err != nil {
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

func (s *Store) New() (Snapshot, *os.File, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Snapshot{}, nil, err
	}
	snap := Snapshot{Version: SnapshotVersion, ID: hex.EncodeToString(id[:]), Workspace: s.workspace, Created: time.Now().UTC()}
	lock, err := s.Lock(snap.ID)
	return snap, lock, err
}

func (s *Store) Lock(id string) (*os.File, error) {
	if !validID(id) {
		return nil, fmt.Errorf("invalid session ID")
	}
	f, err := os.OpenFile(filepath.Join(s.dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	ok, err := tryLock(f)
	if err != nil || !ok {
		f.Close()
		if err == nil {
			err = ErrSessionBusy
		}
		return nil, err
	}
	return f, nil
}

func Release(f *os.File) {
	if f != nil {
		unlock(f)
		f.Close()
	}
}

func (s *Store) Save(snap Snapshot) error {
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
	f, err := os.CreateTemp(s.dir, ".snapshot-")
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
	return replaceSnapshot(f.Name(), filepath.Join(s.dir, snap.ID+".json"))
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
			entries = append(entries, Entry{Snapshot: Snapshot{ID: id, Saved: info.ModTime()}, Problem: problem.Preview})
			continue
		}
		snap, err = s.Sanitize(snap)
		if err != nil {
			return nil, err
		}
		lock, lockErr := s.Lock(id)
		Release(lock)
		entry := Entry{Snapshot: snap, Busy: errors.Is(lockErr, ErrSessionBusy)}
		if lockErr != nil && !entry.Busy {
			problem, filterErr := s.Sanitize(Snapshot{ID: id, Workspace: s.workspace, Version: SnapshotVersion, Preview: "Cannot lock session: " + lockErr.Error()})
			if filterErr != nil {
				return nil, filterErr
			}
			entry.Problem = problem.Preview
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return Departure(entries[i].Snapshot).After(Departure(entries[j].Snapshot)) })
	return entries, nil
}
