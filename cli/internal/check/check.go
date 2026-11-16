// Package check prepares repository integrity checks without command parsing or configuration.
package check

import "github.com/Besi97/cairn-backup/cli/internal/borg"

// Options contains inputs for one whole-repository check without repair.
type Options struct {
	Repository     string
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
	VerifyData     bool
}

// Prepare checks repository consistency and all archive metadata. VerifyData
// additionally requests cryptographic file-content verification.
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
	args := append([]string{"check"}, lockArgs...)
	args = append(args, "--verbose", "--progress")
	if options.VerifyData {
		args = append(args, "--verify-data")
	}
	args = append(args, "--", repository.Location)
	return borg.Request{Args: args, Dir: invocationDir, Access: repository.Access}, nil
}
