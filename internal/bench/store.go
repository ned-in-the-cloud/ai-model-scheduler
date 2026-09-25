package bench

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store persists suites, runs, and datasets as JSON files under a data
// directory. Volumes are tiny (tens of files), so one file per record with
// an atomic rename is simpler and more transparent than a database.
type Store struct {
	dir string
	mu  sync.Mutex
}

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// NewStore creates the directory layout if needed.
func NewStore(dir string) (*Store, error) {
	for _, sub := range []string{"suites", "runs", "datasets"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("creating data dir: %w", err)
		}
	}
	return &Store{dir: dir}, nil
}

// newID returns a sortable, URL-safe identifier: timestamp plus random tail.
func newID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// Short returns the random tail of an ID, for use in job names.
func Short(id string) string {
	if i := strings.LastIndex(id, "-"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func (s *Store) path(kind, id, ext string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("invalid id %q", id)
	}
	return filepath.Join(s.dir, kind, id+ext), nil
}

func (s *Store) writeJSON(kind, id string, v any) error {
	p, err := s.path(kind, id, ".json")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(p, data)
}

func atomicWrite(p string, data []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (s *Store) readJSON(kind, id string, v any) error {
	p, err := s.path(kind, id, ".json")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return json.Unmarshal(data, v)
}

func (s *Store) remove(kind, id, ext string) error {
	p, err := s.path(kind, id, ext)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ids lists record IDs of one kind, newest first (IDs sort by time).
func (s *Store) ids(kind string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, kind))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// Suites

// SaveSuite validates and stores a suite, assigning an ID if new.
func (s *Store) SaveSuite(suite *Suite) error {
	if err := suite.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if suite.ID == "" {
		suite.ID = newID()
		suite.CreatedAt = now
	}
	suite.UpdatedAt = now
	return s.writeJSON("suites", suite.ID, suite)
}

// GetSuite loads one suite.
func (s *Store) GetSuite(id string) (*Suite, error) {
	var suite Suite
	if err := s.readJSON("suites", id, &suite); err != nil {
		return nil, err
	}
	return &suite, nil
}

// ListSuites returns all suites, newest first.
func (s *Store) ListSuites() ([]Suite, error) {
	ids, err := s.ids("suites")
	if err != nil {
		return nil, err
	}
	out := make([]Suite, 0, len(ids))
	for _, id := range ids {
		if suite, err := s.GetSuite(id); err == nil {
			out = append(out, *suite)
		}
	}
	return out, nil
}

// DeleteSuite removes a suite; past runs keep their own copy of its configs.
func (s *Store) DeleteSuite(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove("suites", id, ".json")
}

// Runs

// SaveRun stores a run, assigning an ID if new.
func (s *Store) SaveRun(run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.ID == "" {
		run.ID = newID()
	}
	return s.writeJSON("runs", run.ID, run)
}

// GetRun loads one run.
func (s *Store) GetRun(id string) (*Run, error) {
	var run Run
	if err := s.readJSON("runs", id, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListRuns returns all runs, newest first.
func (s *Store) ListRuns() ([]Run, error) {
	ids, err := s.ids("runs")
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(ids))
	for _, id := range ids {
		if run, err := s.GetRun(id); err == nil {
			out = append(out, *run)
		}
	}
	return out, nil
}

// DeleteRun removes a run record.
func (s *Store) DeleteRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remove("runs", id, ".json")
}

// Datasets

// MaxDatasetBytes bounds uploads. The app has no share access, so a run
// stages the dataset there through small Nomad jobs (see Runner.stageDataset);
// larger sets go on the model share directly and are referenced by path.
const MaxDatasetBytes = 10 << 20

// SaveDataset validates JSONL content (each row needs prompt and expected)
// and stores it with a metadata record.
func (s *Store) SaveDataset(name string, content []byte) (*Dataset, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("dataset name is required")
	}
	if len(content) > MaxDatasetBytes {
		return nil, fmt.Errorf("dataset is %d bytes; uploads are limited to %d MB (put larger files on the model share and use a path)", len(content), MaxDatasetBytes>>20)
	}
	rows, err := countJSONLRows(content)
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, fmt.Errorf("dataset has no rows")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ds := &Dataset{ID: newID(), Name: name, Rows: rows, SizeBytes: int64(len(content)), CreatedAt: time.Now().UTC()}
	p, _ := s.path("datasets", ds.ID, ".jsonl")
	if err := atomicWrite(p, content); err != nil {
		return nil, err
	}
	if err := s.writeJSON("datasets", ds.ID, ds); err != nil {
		return nil, err
	}
	return ds, nil
}

// countJSONLRows validates each non-blank line as an object with string
// prompt and expected fields.
func countJSONLRows(content []byte) (int, error) {
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), MaxDatasetBytes+1)
	n, line := 0, 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var row struct {
			Prompt   any `json:"prompt"`
			Expected any `json:"expected"`
		}
		if err := json.Unmarshal([]byte(text), &row); err != nil {
			return 0, fmt.Errorf("line %d: not valid JSON: %v", line, err)
		}
		if _, ok := row.Prompt.(string); !ok {
			return 0, fmt.Errorf("line %d: \"prompt\" must be a string", line)
		}
		if _, ok := row.Expected.(string); !ok {
			return 0, fmt.Errorf("line %d: \"expected\" must be a string", line)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, nil
}

// GetDataset loads dataset metadata.
func (s *Store) GetDataset(id string) (*Dataset, error) {
	var ds Dataset
	if err := s.readJSON("datasets", id, &ds); err != nil {
		return nil, err
	}
	return &ds, nil
}

// ReadDataset returns the JSONL content.
func (s *Store) ReadDataset(id string) ([]byte, error) {
	p, err := s.path("datasets", id, ".jsonl")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

// ListDatasets returns all datasets, newest first.
func (s *Store) ListDatasets() ([]Dataset, error) {
	ids, err := s.ids("datasets")
	if err != nil {
		return nil, err
	}
	out := make([]Dataset, 0, len(ids))
	for _, id := range ids {
		if ds, err := s.GetDataset(id); err == nil {
			out = append(out, *ds)
		}
	}
	return out, nil
}

// DeleteDataset removes a dataset and its content.
func (s *Store) DeleteDataset(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.remove("datasets", id, ".jsonl"); err != nil {
		return err
	}
	return s.remove("datasets", id, ".json")
}
