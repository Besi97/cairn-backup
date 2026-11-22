// Package workspace temporarily extracts data for a recovery drill.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/restore"
)

type Options struct {
	// Repository carries access settings and an absolute local location (or
	// remote Borg location). Any credential file paths must already be resolved.
	Repository   borg.PreparedRepository
	ArchiveName  string // Already resolved, literal Borg archive name.
	Paths        []string
	WholeArchive bool
	LockWait     *int
}

// CleanupError identifies a temporary directory whose cleanup failed or was
// skipped because Borg's process group could not be confirmed stopped.
type CleanupError struct {
	Path string
	Err  error
}

func (e *CleanupError) Error() string {
	return fmt.Sprintf("clean recovery workspace %q: %v", e.Path, e.Err)
}

func (e *CleanupError) Unwrap() error { return e.Err }

// Run passes the extracted directory to use only after a clean native restore.
// The callback must handle cancellation and finish all its work (including
// subprocesses) before returning. The archive is not a sandbox: callers must
// safely handle extracted entries, including symlinks, rather than follow them.
//
// The result describes Borg, not callback or cleanup success. The error can
// include a CleanupError in addition to an extraction or callback error.
// The system temp directory must be trusted and have enough free space; Run
// does not reserve space or recover leftovers after a crash or SIGKILL.
func Run(ctx context.Context, runner borg.Runner, options Options, diagnostics io.Writer, use func(context.Context, string) error) (result borg.Result, err error) {
	result = borg.Result{Outcome: borg.Failure, ExitCode: -1}
	if err := ctx.Err(); err != nil {
		result.Outcome = borg.Interrupted
		return result, err
	}
	if options.WholeArchive == (len(options.Paths) > 0) {
		return result, errors.New("provide recovery paths or explicitly request the whole archive, not both")
	}
	if use == nil {
		return result, errors.New("recovery workspace requires a consumer")
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return result, fmt.Errorf("resolve recovery temp directory: %w", err)
	}
	temp, err = filepath.EvalSymlinks(temp)
	if err != nil {
		return result, fmt.Errorf("resolve recovery temp directory: %w", err)
	}
	private, err := os.MkdirTemp(temp, "cairn-recovery-")
	if err != nil {
		return result, fmt.Errorf("create recovery workspace: %w", err)
	}
	defer func() {
		var cleanupErr error
		if errors.Is(err, borg.ErrProcessGroupNotStopped) {
			cleanupErr = errors.New("left in place because Borg shutdown could not be confirmed")
		} else {
			cleanupErr = remove(private)
		}
		if cleanupErr != nil {
			err = errors.Join(err, &CleanupError{Path: private, Err: cleanupErr})
		}
	}()
	if err := os.Chmod(private, 0700); err != nil {
		return result, fmt.Errorf("set recovery workspace permissions: %w", err)
	}
	// Restore creates the inner directory. The archive can restore permissions
	// there (including for "."), but cannot change the enclosing private mode.
	destination := filepath.Join(private, "extract")
	plan, err := restore.PrepareExtraction(options.Repository, restore.ExtractionOptions{
		ArchiveName: options.ArchiveName, Destination: destination,
		Paths: options.Paths, LockWait: options.LockWait,
	}, private)
	if err != nil {
		return result, err
	}
	result, err = plan.Run(ctx, runner, false, diagnostics)
	if err != nil {
		return result, err
	}
	if result.Outcome != borg.Success {
		return result, errors.New("recovery extraction reported warnings; checks were not run")
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	err = use(ctx, destination)
	if canceled := ctx.Err(); canceled != nil && !errors.Is(err, canceled) {
		err = errors.Join(err, canceled)
	}
	return result, err
}

func remove(private string) error {
	// Only touch directory modes if removal needs it. Archived directories
	// can be unsearchable or unwritable. WalkDir visits them before reading
	// their children and does not follow symlinks. Never chmod a file or link.
	if err := os.RemoveAll(private); !errors.Is(err, fs.ErrPermission) {
		return err
	}
	walkErr := filepath.WalkDir(private, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			return os.Chmod(path, 0700)
		}
		return err
	})
	if err := os.RemoveAll(private); err != nil {
		return errors.Join(err, walkErr)
	}
	return nil
}
