package cli

import (
	"sync"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

// History is lazy so browsing, validation, and rejected arguments leave no state.
type historyRecorder struct {
	mu          sync.Mutex
	store       *history.Store
	warn        func()
	lastWarning time.Time
}

func (r *historyRecorder) observe(event lifecycle.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	if r.store == nil {
		r.store, err = history.Open()
	}
	if err == nil {
		err = r.store.Observe(event)
	}
	if err != nil {
		r.warning()
	}
	return err
}

func (r *historyRecorder) warning() {
	now := time.Now()
	if r.lastWarning.IsZero() || now.Sub(r.lastWarning) >= time.Minute {
		r.lastWarning = now
		r.warn()
	}
}

func (r *historyRecorder) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store != nil && r.store.Close() != nil {
		r.warning()
	}
}
