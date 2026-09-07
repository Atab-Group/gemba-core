package atab

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GembaCore/gemba-core/core"
)

// persistedSchema is the on-disk shape of a saved snapshot. A file
// written by a different schema is ignored rather than migrated: the
// snapshot is a cache of something GitHub still holds, so discarding one
// costs a refresh and guessing at one costs correctness.
const persistedSchema = 1

// PersistedSnapshot is one source's cache as written to disk.
//
// It carries the two timestamps the cache reasons about rather than just
// the issues, because a restored snapshot that cannot say when it was
// taken cannot be judged. Restoring one with the clock reset to now
// would present month-old issues as current, which is the failure this
// whole mechanism exists to avoid.
type PersistedSnapshot struct {
	Schema int      `json:"schema"`
	Source SourceID `json:"source"`
	// Fingerprint identifies the question the snapshot answers: the org,
	// the board and the repositories it was read from. A snapshot whose
	// fingerprint no longer matches the configured source is describing
	// a different source under the same id and is discarded.
	Fingerprint   string    `json:"fingerprint"`
	LastSuccess   time.Time `json:"last_success"`
	LastFullFetch time.Time `json:"last_full_fetch"`
	Issues        []Issue   `json:"issues"`
}

// SnapshotStore persists a source's snapshot between processes.
//
// It exists so a restart does not start from an empty cache. An empty
// cache always fetches full, and a full fetch is both the slowest and
// the most expensive thing this adaptor does, so a process that restarts
// while GitHub is throttling it would otherwise serve nothing at all
// until the budget returns.
type SnapshotStore interface {
	// Load returns the stored snapshot for id. The bool is false when
	// nothing is stored, which is not an error.
	Load(id SourceID) (PersistedSnapshot, bool, error)
	// Save replaces the stored snapshot for snap.Source.
	Save(snap PersistedSnapshot) error
}

// FileStore keeps one JSON file per source under a directory.
type FileStore struct {
	dir string
}

var _ SnapshotStore = (*FileStore)(nil)

// NewFileStore returns a store writing under dir, creating it if needed.
func NewFileStore(dir string) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, core.NewAdaptorError(core.KindValidation,
			"atab: snapshot store needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not create the snapshot directory %q", dir)
	}
	return &FileStore{dir: dir}, nil
}

// path is the file one source is stored in. Source ids are validated to
// the lowercase-hyphen slug shape, so one cannot escape the directory.
func (s *FileStore) path(id SourceID) string {
	return filepath.Join(s.dir, string(id)+".json")
}

// Load reads the stored snapshot for id.
//
// A file that is absent, unreadable as JSON, or written by another
// schema reports "nothing stored" rather than an error. The snapshot is
// a cache: refusing to start because a cache file is corrupt would turn
// a recoverable annoyance into an outage.
func (s *FileStore) Load(id SourceID) (PersistedSnapshot, bool, error) {
	raw, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return PersistedSnapshot{}, false, nil
		}
		return PersistedSnapshot{}, false, core.WrapAdaptorError(
			core.KindRequestFailed, err,
			"atab: could not read the stored snapshot for %q", id)
	}
	var snap PersistedSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return PersistedSnapshot{}, false, nil
	}
	if snap.Schema != persistedSchema || snap.Source != id {
		return PersistedSnapshot{}, false, nil
	}
	return snap, true, nil
}

// Save writes the snapshot for snap.Source.
//
// The write is atomic: a snapshot of a large source is several megabytes
// and a process killed mid-write would otherwise leave a truncated file
// that the next boot reads as a source with a handful of issues in it.
func (s *FileStore) Save(snap PersistedSnapshot) error {
	snap.Schema = persistedSchema
	raw, err := json.Marshal(snap)
	if err != nil {
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not encode the snapshot for %q", snap.Source)
	}

	final := s.path(snap.Source)
	tmp, err := os.CreateTemp(s.dir, string(snap.Source)+".*.tmp")
	if err != nil {
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not stage the snapshot for %q", snap.Source)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not write the snapshot for %q", snap.Source)
	}
	if err := tmp.Close(); err != nil {
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not close the staged snapshot for %q", snap.Source)
	}
	if err := os.Rename(name, final); err != nil {
		return core.WrapAdaptorError(core.KindRequestFailed, err,
			"atab: could not install the snapshot for %q", snap.Source)
	}
	return nil
}

// Fingerprint identifies the source a snapshot was read from.
//
// Org, board and repository set are all in it because each of them
// changes which issues belong on the board. Restoring a snapshot taken
// before a repository was removed from the source would put that
// repository's issues back, and nothing later in the refresh cycle would
// take them off again: an incremental fetch never removes anything, and
// a full one only replaces what it was asked for.
func (c SourceConfig) Fingerprint() string {
	repos := append([]string(nil), c.Repos...)
	sort.Strings(repos)

	fields := make([]string, 0, len(c.FieldNames))
	for k, v := range c.FieldNames {
		fields = append(fields, k+"="+v)
	}
	sort.Strings(fields)

	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s",
		c.Org, c.ProjectNumber, strings.Join(repos, ","), strings.Join(fields, ","))))
	return fmt.Sprintf("%x", sum[:8])
}
