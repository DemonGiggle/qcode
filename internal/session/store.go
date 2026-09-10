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
	"time"
)

const SnapshotVersion = 1

type SavedAgent struct {
	Summary Summary
	State   json.RawMessage
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

type Store struct{ dir, workspace string }

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
			err = fmt.Errorf("session is open in another process")
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
			entries = append(entries, Entry{Snapshot: Snapshot{ID: id, Saved: info.ModTime()}, Problem: "Unreadable or incompatible snapshot: " + err.Error()})
			continue
		}
		lock, lockErr := s.Lock(id)
		Release(lock)
		entries = append(entries, Entry{Snapshot: snap, Busy: lockErr != nil})
	}
	sort.Slice(entries, func(i, j int) bool { return Departure(entries[i].Snapshot).After(Departure(entries[j].Snapshot)) })
	return entries, nil
}
