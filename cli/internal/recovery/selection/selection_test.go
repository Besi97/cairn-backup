package selection

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestRevalidate(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "borg")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'borg 1.2.8'; exit 0; fi\nprintf '%s' \"$SELECTION_FIXTURE\"\nexit \"$SELECTION_EXIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("1", 64)
	other := strings.Repeat("2", 64)
	candidate := Candidate{Name: "chosen", ArchiveID: id, RepositoryID: id}
	listing := func(name, archiveID, repositoryID string) string {
		return `{"archives":[{"barchive":"` + name + `","id":"` + archiveID + `"}],"repository":{"id":"` + repositoryID + `"}}`
	}
	options := Options{Repository: borg.PreparedRepository{Location: filepath.Join(dir, "repo")}, Dir: dir}
	for _, tc := range []struct {
		name, output, exit string
		want               error
		ok                 bool
	}{
		{"unchanged", listing("chosen", id, id), "0", nil, true},
		{"disappeared", listing("different", id, id), "0", ErrNotFound, false},
		{"archive replaced", listing("chosen", other, id), "0", ErrChanged, false},
		{"repository replaced", listing("chosen", id, other), "0", ErrChanged, false},
		{"warning", listing("chosen", id, id), "1", nil, false},
		{"failure", listing("chosen", id, id), "2", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SELECTION_FIXTURE", tc.output)
			t.Setenv("SELECTION_EXIT", tc.exit)
			result, err := Revalidate(t.Context(), borg.Runner{Executable: binary}, options, candidate, nil)
			if (err == nil) != tc.ok || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.ok && result.Outcome != borg.Success) {
				t.Fatalf("result = %+v, err = %v", result, err)
			}
		})
	}
	if _, err := Revalidate(t.Context(), borg.Runner{Executable: binary}, options, Candidate{}, nil); err == nil {
		t.Fatal("accepted invalid candidate")
	}
}

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
