package history

import "github.com/Besi97/cairn-backup/cli/internal/runlock"

// Observation separates recorded outcomes from momentary execution evidence.
// Neither a missing owner nor an unobserved lock establishes that Borg exited.
type Observation struct {
	Summary Summary
	runlock.Observation
}

// Inspect returns newest-first summaries with conservative owner/lock evidence.
// It never changes history or acquires execution locks. Storage failures retain
// List's ErrUnavailable contract; unavailable execution evidence stays unknown.
func (s *Store) Inspect() ([]Observation, error) {
	runs, err := s.List()
	if err != nil {
		return nil, err
	}
	inspector := runlock.NewInspector()
	defer inspector.Close()
	result := make([]Observation, 0, len(runs))
	for _, run := range runs {
		result = append(result, Observation{Summary: run, Observation: inspector.Inspect(run.Execution)})
	}
	return result, nil
}
