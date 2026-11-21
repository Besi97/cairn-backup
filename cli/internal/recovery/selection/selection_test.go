package selection

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestResolveDoesNotReturnPartialCandidate(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "borg")
	if err := os.WriteFile(binary, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then
	printf 'borg 1.2.8\n'
	exit 0
fi
printf '%s' "$SELECTION_FIXTURE"
exit "$SELECTION_EXIT"
`), 0700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 64)
	listing := `{"archives":[{"barchive":"chosen","id":"` + id + `"}],"repository":{"id":"` + id + `"}}`
	for _, tc := range []struct {
		name, output, exit string
		outcome            borg.Outcome
	}{
		{"late warning", listing, "1", borg.Warning},
		{"late failure", listing, "2", borg.Failure},
		{"partial output", listing[:len(listing)-1], "0", borg.Failure},
		{"trailing output", listing + "{}", "0", borg.Failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SELECTION_FIXTURE", tc.output)
			t.Setenv("SELECTION_EXIT", tc.exit)
			repo := borg.PreparedRepository{Location: filepath.Join(dir, "repo")}
			got, result, err := Resolve(t.Context(), borg.Runner{Executable: binary},
				Options{Repository: repo, Dir: dir}, Selector{Exact: "chosen"}, nil)
			if err == nil || errors.Is(err, ErrNotFound) || got != (Candidate{}) || result.Outcome != tc.outcome {
				t.Fatalf("got %+v, %+v, %v", got, result, err)
			}
		})
	}
}
