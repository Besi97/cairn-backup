// Package history keeps bounded local summaries, never execution authority.
package history

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

const (
	MaxRuns          = 100
	MaxBytes         = 1 << 20
	MaxLabelBytes    = 256
	MaxRecoveryTasks = 16
	lockWait         = 100 * time.Millisecond
)

// ErrUnavailable deliberately excludes filesystem paths and underlying errors.
var ErrUnavailable = errors.New("local run history unavailable")

type TaskCounts struct {
	Success     uint64 `json:"success"`
	Warning     uint64 `json:"warning"`
	Failure     uint64 `json:"failure"`
	Interrupted uint64 `json:"interrupted"`
	Skipped     uint64 `json:"skipped"`
}

type RecoveryTask struct {
	Stage    int                    `json:"stage"`
	Task     int                    `json:"task"`
	Evidence report.HistoryEvidence `json:"evidence"`
}

// Incomplete means no terminal event was recorded, not that work is still alive.
// Partial means an event sequence gap was observed; counts may be incomplete.
type Summary struct {
	RunID           string            `json:"run_id"`
	ConfigID        string            `json:"config_id,omitempty"`
	Workflow        string            `json:"workflow,omitempty"`
	Operation       string            `json:"operation,omitempty"`
	Source          lifecycle.Source  `json:"source"`
	ScheduledAt     time.Time         `json:"scheduled_at,omitempty"`
	Preview         bool              `json:"preview,omitempty"`
	FirstObserved   time.Time         `json:"first_observed"`
	LastObserved    time.Time         `json:"last_observed"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
	Sequence        uint64            `json:"sequence"`
	Outcome         lifecycle.Outcome `json:"outcome,omitempty"`
	Reason          lifecycle.Reason  `json:"reason,omitempty"`
	Incomplete      bool              `json:"incomplete"`
	Partial         bool              `json:"partial,omitempty"`
	Tasks           TaskCounts        `json:"tasks"`
	Execution       *runlock.Evidence `json:"execution,omitempty"`
	Recovery        []RecoveryTask    `json:"recovery,omitempty"`
	RecoveryOmitted uint64            `json:"recovery_omitted,omitempty"`
}

type snapshot struct {
	Version int       `json:"version"`
	Runs    []Summary `json:"runs"`
}

type Store struct {
	mu        sync.Mutex
	directory *os.File
	readOnly  bool
	closed    bool
}

func Open() (*Store, error) {
	state, err := statePath()
	if err != nil {
		return nil, err
	}
	return open(state)
}

// OpenReadOnly opens existing history without creating files or taking locks.
// Missing storage is empty; unsafe or unreadable storage is unavailable.
// Reopen on refresh to discover directories that were absent at open time.
func OpenReadOnly() (*Store, error) {
	state, err := statePath()
	if err != nil {
		return nil, err
	}
	return openStore(state, true)
}

func statePath() (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home := os.Getenv("HOME")
		if !filepath.IsAbs(home) {
			return "", ErrUnavailable
		}
		state = filepath.Join(home, ".local", "state")
	}
	return state, nil
}

func open(state string) (*Store, error) {
	return openStore(state, false)
}

func openStore(state string, readOnly bool) (*Store, error) {
	if !readOnly {
		if err := os.MkdirAll(state, 0700); err != nil {
			return nil, ErrUnavailable
		}
	}
	fd, err := syscall.Open(state, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if readOnly && errors.Is(err, os.ErrNotExist) {
		return &Store{readOnly: true}, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	base := os.NewFile(uintptr(fd), state)
	defer base.Close()
	info, err := base.Stat()
	if err != nil || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return nil, ErrUnavailable
	}
	cairn, err := child(base, "cairn", !readOnly)
	if readOnly && errors.Is(err, syscall.ENOENT) {
		return &Store{readOnly: true}, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer cairn.Close()
	directory, err := child(cairn, "history", !readOnly)
	if readOnly && errors.Is(err, syscall.ENOENT) {
		return &Store{readOnly: true}, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Store{directory: directory, readOnly: readOnly}, nil
}

func child(parent *os.File, name string, create bool) (*os.File, error) {
	if create {
		if err := syscall.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, ErrUnavailable
		}
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.directory == nil {
		return nil
	}
	err := s.directory.Close()
	s.directory = nil
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Store) file(name string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(int(s.directory.Fd()), name, flags|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil {
		f.Close()
		return nil, ErrUnavailable
	}
	// Atomic replacement can unlink an already-open read-only snapshot.
	retired := s.readOnly && flags == syscall.O_RDONLY && st.Nlink == 0
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Uid != uint32(os.Geteuid()) ||
		st.Mode&07777 != 0600 || (st.Nlink != 1 && !retired) {
		f.Close()
		return nil, ErrUnavailable
	}
	return f, nil
}

func (s *Store) lock() (*os.File, error) {
	if s.directory == nil {
		return nil, ErrUnavailable
	}
	f, err := s.file("history.lock", syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return nil, ErrUnavailable
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		remaining := time.Until(deadline)
		if !errors.Is(err, syscall.EWOULDBLOCK) || remaining <= 0 {
			f.Close()
			return nil, ErrUnavailable
		}
		time.Sleep(min(time.Millisecond, remaining))
	}
}

func (s *Store) load() (snapshot, error) {
	data := snapshot{Version: 1, Runs: []Summary{}}
	f, err := s.file("runs.json", syscall.O_RDONLY)
	if errors.Is(err, syscall.ENOENT) {
		return data, nil
	}
	if err != nil {
		return data, ErrUnavailable
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes/2+1))
	if err != nil || len(raw) > MaxBytes/2 {
		return snapshot{}, ErrUnavailable
	}
	data = snapshot{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&data) != nil || decoder.Decode(new(any)) != io.EOF ||
		data.Version != 1 || data.Runs == nil || len(data.Runs) > MaxRuns {
		return snapshot{}, ErrUnavailable
	}
	seen := make(map[string]bool)
	for _, run := range data.Runs {
		if !validSummary(run) || seen[run.RunID] {
			return snapshot{}, ErrUnavailable
		}
		seen[run.RunID] = true
	}
	return data, nil
}

// List returns newest first. Reading never reconciles or changes run state.
func (s *Store) List() ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrUnavailable
	}
	if s.readOnly && s.directory == nil {
		return []Summary{}, nil
	}
	if !s.readOnly {
		lock, err := s.lock()
		if err != nil {
			return nil, err
		}
		defer lock.Close()
	}
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	sortRuns(data.Runs)
	return data.Runs, nil
}

func sortRuns(runs []Summary) {
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].LastObserved.Equal(runs[j].LastObserved) {
			return runs[i].RunID > runs[j].RunID
		}
		return runs[i].LastObserved.After(runs[j].LastObserved)
	})
}

func (s *Store) Observe(event lifecycle.Event) error {
	if !validEvent(event) {
		return ErrUnavailable
	}
	var evidence report.HistoryEvidence
	var evidenceErr error
	if event.Recovery != nil {
		evidence, evidenceErr = report.ForHistory(*event.Recovery)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOnly || s.closed {
		return ErrUnavailable
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := s.load()
	if err != nil {
		return err
	}
	index := -1
	for i := range data.Runs {
		if data.Runs[i].RunID == event.RunID {
			index = i
			break
		}
	}
	if index < 0 {
		data.Runs = append(data.Runs, Summary{
			RunID: event.RunID, ConfigID: event.ConfigID, Workflow: event.Workflow,
			Source: event.Source, ScheduledAt: event.ScheduledAt, Preview: event.Preview,
			FirstObserved: event.Time, Incomplete: true,
			Execution: copyEvidence(event.Execution),
		})
		index = len(data.Runs) - 1
	}
	run := &data.Runs[index]
	if event.Sequence <= run.Sequence {
		return nil
	}
	run.Partial = run.Partial || event.Sequence != run.Sequence+1
	run.Sequence, run.LastObserved = event.Sequence, event.Time
	if event.Workflow == "" {
		run.Operation = event.Operation
	}
	switch event.Kind {
	case lifecycle.RunStarted:
		at := event.Time
		run.StartedAt = &at
	case lifecycle.RunFinished, lifecycle.RunSkipped:
		at := event.Time
		run.FinishedAt = &at
		run.Outcome, run.Reason, run.Incomplete = event.Outcome, event.Reason, false
	case lifecycle.TaskFinished, lifecycle.TaskSkipped:
		switch event.Outcome {
		case lifecycle.Success:
			run.Tasks.Success++
		case lifecycle.Warning:
			run.Tasks.Warning++
		case lifecycle.Failure:
			run.Tasks.Failure++
		case lifecycle.Interrupted:
			run.Tasks.Interrupted++
		case lifecycle.Skipped:
			run.Tasks.Skipped++
		}
	}
	if event.Recovery != nil {
		if evidenceErr != nil || event.Kind != lifecycle.TaskFinished || event.Stage <= 0 || event.Task <= 0 || len(run.Recovery) >= MaxRecoveryTasks {
			omitRecovery(run)
		} else {
			run.Recovery = append(run.Recovery, RecoveryTask{event.Stage, event.Task, evidence})
		}
	}
	sortRuns(data.Runs)
	if len(data.Runs) > MaxRuns {
		data.Runs = data.Runs[:MaxRuns]
	}
	raw, err := json.Marshal(data)
	// Discard identifying details before recovery records or run summaries.
	need := len(raw) - MaxBytes/2
	detailsDropped := false
	for i := len(data.Runs) - 1; i >= 0 && err == nil && need > 0; i-- {
		for j := len(data.Runs[i].Recovery) - 1; j >= 0 && need > 0; j-- {
			e := &data.Runs[i].Recovery[j].Evidence
			if e.Details == nil {
				continue
			}
			before, marshalErr := json.Marshal(e)
			e.Details, e.DetailsOmitted = nil, true
			detailsDropped = true
			after, afterErr := json.Marshal(e)
			if marshalErr != nil || afterErr != nil {
				return ErrUnavailable
			}
			need -= len(before) - len(after)
		}
	}
	if detailsDropped {
		raw, err = json.Marshal(data)
	}
	for err == nil && len(raw) > MaxBytes/2 {
		removed := false
		for i := len(data.Runs) - 1; i >= 0; i-- {
			r := &data.Runs[i]
			if len(r.Recovery) > 0 {
				r.Recovery = r.Recovery[:len(r.Recovery)-1]
				omitRecovery(r)
				removed = true
				break
			}
		}
		if !removed {
			break
		}
		raw, err = json.Marshal(data)
	}
	// Atomic replacement needs one bounded scratch copy alongside the snapshot.
	if err != nil || len(raw) > MaxBytes/2 {
		return ErrUnavailable
	}
	pending, err := s.file("runs.pending", syscall.O_RDWR|syscall.O_CREAT)
	if err != nil {
		return ErrUnavailable
	}
	defer pending.Close()
	if pending.Truncate(0) != nil {
		return ErrUnavailable
	}
	if _, err := pending.Write(raw); err != nil {
		return ErrUnavailable
	}
	if pending.Sync() != nil || pending.Close() != nil {
		return ErrUnavailable
	}
	fd := int(s.directory.Fd())
	if syscall.Renameat(fd, "runs.pending", fd, "runs.json") != nil || s.directory.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}

func omitRecovery(r *Summary) {
	if r.RecoveryOmitted < ^uint64(0) {
		r.RecoveryOmitted++
	}
}

func label(s string) bool { return len(s) <= MaxLabelBytes && utf8.ValidString(s) }

func digest(s string) bool {
	if s == "" {
		return true
	}
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32
}

func validSummary(s Summary) bool {
	if len(s.Recovery) > MaxRecoveryTasks {
		return false
	}
	for _, r := range s.Recovery {
		if r.Stage <= 0 || r.Task <= 0 || !r.Evidence.Valid() {
			return false
		}
	}
	return !s.FirstObserved.IsZero() && s.Incomplete == (s.FinishedAt == nil) &&
		validEvent(lifecycle.Event{
			RunID: s.RunID, ConfigID: s.ConfigID, Workflow: s.Workflow, Operation: s.Operation,
			Source: s.Source, Sequence: s.Sequence, Time: s.LastObserved,
			Kind: lifecycle.RunStarted, Outcome: s.Outcome, Reason: s.Reason,
			Execution: s.Execution,
		})
}

func operation(s string) bool {
	return s == "backup" || s == "prune" || s == "compact" || s == "check" || s == "restore" || s == "recovery" || s == "recovery_check"
}

func validEvent(e lifecycle.Event) bool {
	if e.Execution != nil && (!e.Execution.Valid() || e.ConfigID == "" || e.Workflow == "") {
		return false
	}
	if e.RunID == "" || !label(e.RunID) || !label(e.Workflow) || !digest(e.ConfigID) ||
		e.Sequence == 0 || e.Time.IsZero() || (e.Operation != "" && !operation(e.Operation)) ||
		(e.Source != lifecycle.Manual && e.Source != lifecycle.Scheduled) {
		return false
	}
	switch e.Kind {
	case lifecycle.RunStarted, lifecycle.RunFinished, lifecycle.RunSkipped,
		lifecycle.StageStarted, lifecycle.StageFinished, lifecycle.StageSkipped,
		lifecycle.TaskStarted, lifecycle.TaskFinished, lifecycle.TaskSkipped:
	default:
		return false
	}
	switch e.Outcome {
	case "", lifecycle.Success, lifecycle.Warning, lifecycle.Failure, lifecycle.Interrupted, lifecycle.Skipped:
	default:
		return false
	}
	switch e.Reason {
	case "", lifecycle.Busy, lifecycle.Expired, lifecycle.PriorWarning, lifecycle.PriorFailure,
		lifecycle.Cancelled, lifecycle.PreparationFailed, lifecycle.Unavailable:
	default:
		return false
	}
	return true
}

func copyEvidence(e *runlock.Evidence) *runlock.Evidence {
	if e == nil {
		return nil
	}
	copy := *e
	return &copy
}
