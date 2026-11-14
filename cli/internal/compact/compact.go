// Package compact prepares standalone repository compaction without command
// parsing or configuration.
package compact

import "github.com/Besi97/cairn-backup/cli/internal/borg"

// Options contains only inputs for one repository-wide compaction.
type Options struct {
	Repository     string
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
}

// Prepare builds one native compact operation. Borg retains its default
// threshold; compaction never selects or deletes archives.
func Prepare(options Options, invocationDir string) (borg.Request, error) {
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
	args := append([]string{"compact"}, lockArgs...)
	args = append(args, "--verbose", "--progress", "--", repository.Location)
	return borg.Request{Args: args, Dir: invocationDir, Access: repository.Access}, nil
}
