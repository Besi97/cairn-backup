// Package archive streams archive and file metadata without CLI or configuration.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

// DisplayText is Borg's potentially lossy JSON text, not a byte-exact name or
// restore selector. Consumers must not derive extraction paths from it.
type DisplayText string

type Archive struct {
	Name  DisplayText `json:"name"`
	ID    string      `json:"id"`
	Start string      `json:"start"`
}

type File struct {
	Path       DisplayText `json:"path"`
	Type       string      `json:"type"`
	Mode       string      `json:"mode"`
	Size       int64       `json:"size"`
	Mtime      string      `json:"mtime"`
	UID        int64       `json:"uid"`
	GID        int64       `json:"gid"`
	LinkTarget DisplayText `json:"linktarget"`
	Healthy    bool        `json:"healthy"`
}

type Options struct {
	Repository     string
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
}

// PrepareList includes checkpoint archives and retains Borg's timestamp ordering.
func PrepareList(options Options, invocationDir string) (borg.Request, error) {
	return prepare(options, invocationDir, "", false)
}

// PrepareInspect inspects metadata only; it does not read or extract file data.
func PrepareInspect(options Options, invocationDir, name string) (borg.Request, error) {
	if name == "" {
		return borg.Request{}, errors.New("--archive-name is required and must not be empty")
	}
	return prepare(options, invocationDir, name, true)
}

func prepare(options Options, invocationDir, name string, inspect bool) (borg.Request, error) {
	repository, err := borg.PrepareRepository(borg.RepositoryOptions{
		Repository: options.Repository, PassphraseFile: options.PassphraseFile, IdentityFile: options.IdentityFile,
	}, invocationDir)
	if err != nil {
		return borg.Request{}, err
	}
	lockArgs, err := borg.LockWaitArgs(options.LockWait)
	if err != nil {
		return borg.Request{}, err
	}
	args := append([]string{"list"}, lockArgs...)
	location := repository.Location
	if inspect {
		args = append(args, "--json-lines", "--format", "{size}{mtime}")
		location += "::" + name
	} else {
		args = append(args, "--json", "--consider-checkpoints")
	}
	args = append(args, "--", location)
	return borg.Request{Args: args, Dir: invocationDir, Access: repository.Access}, nil
}

// List calls emit serially, with backpressure, before Borg finishes. No records
// are retained. Previously emitted records remain partial until Run succeeds or
// completes with warnings. A callback error stops Borg and is an execution
// failure unless an earlier parent cancellation selected the interruption.
func List(ctx context.Context, runner borg.Runner, request borg.Request, emit func(Archive) error) (borg.Result, error) {
	return stream(ctx, runner, request, func(r io.Reader) error {
		return decodeArchives(r, func(entry Archive) error {
			if err := emit(entry); err != nil {
				return fmt.Errorf("consume archive record: %w", err)
			}
			return nil
		})
	})
}

// Inspect has the same streaming contract as List, for file metadata.
func Inspect(ctx context.Context, runner borg.Runner, request borg.Request, emit func(File) error) (borg.Result, error) {
	return stream(ctx, runner, request, func(r io.Reader) error {
		return decodeFiles(r, func(entry File) error {
			if err := emit(entry); err != nil {
				return fmt.Errorf("consume file record: %w", err)
			}
			return nil
		})
	})
}

func stream(ctx context.Context, runner borg.Runner, request borg.Request, decode func(io.Reader) error) (borg.Result, error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// Distinguish decoder-initiated cancellation from parent cancellation, even
	// if the parent is cancelled later while Borg is shutting down.
	decodeStopped := errors.New("listing decoder stopped")
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := decode(reader)
		_ = reader.CloseWithError(err)
		if err != nil {
			cancel(decodeStopped)
		}
		done <- err
	}()
	request.Stdout = writer
	result, runErr := runner.Run(runCtx, request)
	_ = writer.Close()
	decodeErr := <-done
	if decodeErr != nil {
		if result.Outcome == borg.Interrupted && borg.IsCancellation(runErr) && context.Cause(runCtx) != decodeStopped {
			if decodeErr == io.EOF {
				return result, runErr
			}
			return result, errors.Join(runErr, &borg.SecondaryError{Err: fmt.Errorf("read Borg listing: %w", decodeErr)})
		}
		if runErr != nil && (decodeErr == io.EOF || decodeErr == io.ErrUnexpectedEOF) {
			return result, runErr
		}
		result.Outcome = borg.Failure
		return result, fmt.Errorf("read Borg listing: %w", errors.Join(decodeErr, runErr))
	}
	return result, runErr
}
