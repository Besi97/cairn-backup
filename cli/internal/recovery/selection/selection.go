// Package selection resolves recovery archive candidates from a Borg manifest.
package selection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

// ErrNotFound means a successful listing contained no matching archive.
var ErrNotFound = errors.New("no matching recovery archive")

// ErrChanged means the selected repository or archive identity changed.
var ErrChanged = errors.New("recovery archive identity changed")

// Selector requires exactly one nonempty literal byte string. Exact can name a
// checkpoint explicitly; LatestPrefix skips Borg-generated checkpoint names.
type Selector struct {
	Exact        string
	LatestPrefix string
}

// Candidate identifies a point-in-time manifest entry, not an extraction.
// Name is a byte string, possibly invalid UTF-8. Do not use it as display text
// or JSON text, or interpolate it unescaped into a Borg location.
type Candidate struct {
	Name         string
	ArchiveID    string
	RepositoryID string
}

// Options takes a prepared repository so callers can reuse their access policy.
// Dir is the absolute invocation directory for Borg.
type Options struct {
	Repository borg.PreparedRepository
	Dir        string
	LockWait   *int
}

// Resolve picks the latest manifest timestamp for a prefix. Equal timestamps
// use the last name in Borg's ascending name order. A failed or warning listing
// produces no candidate, even if it emitted matching entries.
//
// This is read-only. Borg releases its repository lock when the listing ends;
// see Revalidate and the integration contract for the resulting limitations.
func Resolve(ctx context.Context, runner borg.Runner, options Options, selector Selector, diagnostics io.Writer) (Candidate, borg.Result, error) {
	failed := borg.Result{Outcome: borg.Failure, ExitCode: -1}
	if (selector.Exact == "") == (selector.LatestPrefix == "") ||
		strings.ContainsAny(selector.Exact+selector.LatestPrefix, "/\x00") {
		return Candidate{}, failed, errors.New("provide exactly one nonempty literal archive name or prefix without slash or NUL")
	}
	if location := options.Repository.Location; location == "" || strings.HasPrefix(location, ":") ||
		strings.ContainsAny(location, "{}\x00") || !filepath.IsAbs(options.Dir) {
		return Candidate{}, failed, errors.New("recovery selection requires a prepared literal repository and absolute working directory")
	}
	lockArgs, err := borg.LockWaitArgs(options.LockWait)
	if err != nil {
		return Candidate{}, failed, err
	}
	args := append([]string{"list"}, lockArgs...)
	// Borg's default checkpoint filter excludes *any* occurrence of
	// ".checkpoint". List everything so only the generated suffix is excluded.
	args = append(args, "--json", "--consider-checkpoints", "--sort-by", "timestamp,name", "--", options.Repository.Location)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	type decoded struct {
		candidate Candidate
		err       error
	}
	done := make(chan decoded, 1)
	go func() {
		candidate, err := readManifest(reader, selector)
		_ = reader.CloseWithError(err)
		if err != nil {
			cancel()
		}
		done <- decoded{candidate, err}
	}()
	result, runErr := runner.Run(runCtx, borg.Request{
		Args: args, Dir: options.Dir, Access: options.Repository.Access,
		// Borg's JSON uses ASCII escapes, including for surrogateescape bytes.
		Env: map[string]string{"PYTHONIOENCODING": "utf-8"}, Stdout: writer, Diagnostics: diagnostics,
	})
	_ = writer.Close()
	listing := <-done
	if ctx.Err() != nil {
		result.Outcome = borg.Interrupted
		return Candidate{}, result, errors.Join(ctx.Err(), runErr)
	}
	if listing.err != nil && !errors.Is(listing.err, io.EOF) && !errors.Is(listing.err, io.ErrUnexpectedEOF) {
		result.Outcome = borg.Failure
		return Candidate{}, result, fmt.Errorf("read recovery archive listing: %w", errors.Join(listing.err, runErr))
	}
	if runErr != nil {
		return Candidate{}, result, runErr
	}
	if result.Outcome == borg.Warning {
		return Candidate{}, result, errors.New("Borg reported warnings during recovery archive selection")
	}
	if result.Outcome != borg.Success {
		return Candidate{}, result, errors.New("Borg did not complete recovery archive selection successfully")
	}
	if listing.err != nil {
		result.Outcome = borg.Failure
		return Candidate{}, result, fmt.Errorf("read recovery archive listing: %w", listing.err)
	}
	if listing.candidate.Name == "" {
		result.Outcome = borg.Failure
		return Candidate{}, result, ErrNotFound
	}
	return listing.candidate, result, nil
}

// Revalidate lists the same repository and exact name again and compares both
// IDs. Disappearance, changed identity, warnings and failures all fail closed;
// it never selects a replacement or repeats a latest-prefix selection.
//
// Use the same Options as Resolve, immediately before and after extraction.
// These read-only checks detect observed changes in cooperative workflows;
// they cannot detect replace-and-revert or ensure that separate preflight and
// extraction commands saw the checked archive.
func Revalidate(ctx context.Context, runner borg.Runner, options Options, candidate Candidate, diagnostics io.Writer) (borg.Result, error) {
	if candidate.Name == "" || !validID(candidate.RepositoryID) || !validID(candidate.ArchiveID) {
		return borg.Result{Outcome: borg.Failure, ExitCode: -1}, errors.New("invalid recovery archive candidate")
	}
	got, result, err := Resolve(ctx, runner, options, Selector{Exact: candidate.Name}, diagnostics)
	if err != nil {
		return result, err
	}
	if got != candidate {
		result.Outcome = borg.Failure
		return result, ErrChanged
	}
	return result, nil
}

var checkpoint = regexp.MustCompile(`\.checkpoint(\.[0-9]+)?$`)

func (s Selector) matches(name string) bool {
	if s.Exact != "" {
		return name == s.Exact
	}
	return strings.HasPrefix(name, s.LatestPrefix) && !checkpoint.MatchString(name)
}
