// Package prune prepares standalone retention operations without command parsing
// or configuration. Borg remains responsible for archive selection and retention.
package prune

import (
	"errors"
	"math/big"
	"regexp"
	"strconv"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

// Retention uses Borg's ordering and count semantics: zero disables a rule and
// negative counts retain all available periods for that rule.
type Retention struct {
	Within   string
	Secondly int
	Minutely int
	Hourly   int
	Daily    int
	Weekly   int
	Monthly  int
	Yearly   int
}

// Options contains only inputs for one prune operation.
type Options struct {
	Repository     string
	ArchivePrefix  string
	Retention      Retention
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
	DryRun         bool
}

// Prepared identifies the unexpanded archive selector and its Borg invocation.
type Prepared struct {
	Selector string
	Request  borg.Request
}

// Prepare validates explicit scope and policy, then builds a single native prune.
// It never lists archives itself, prompts for confirmation, or invokes compact.
func Prepare(options Options, invocationDir string) (Prepared, error) {
	repository, err := borg.PrepareRepository(borg.RepositoryOptions{
		Repository: options.Repository, PassphraseFile: options.PassphraseFile, IdentityFile: options.IdentityFile,
	}, invocationDir)
	if err != nil {
		return Prepared{}, err
	}
	if options.ArchivePrefix == "" {
		return Prepared{}, errors.New("--archive-prefix is required and must not be empty")
	}
	retentionArgs, err := options.Retention.args()
	if err != nil {
		return Prepared{}, err
	}
	lockArgs, err := borg.LockWaitArgs(options.LockWait)
	if err != nil {
		return Prepared{}, err
	}
	selector := options.ArchivePrefix + "-*"
	args := append([]string{"prune"}, lockArgs...)
	args = append(args, "--list")
	if !options.DryRun {
		args = append(args, "--stats")
	}
	args = append(args, "--glob-archives="+selector)
	args = append(args, retentionArgs...)
	if options.DryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--", repository.Location)
	return Prepared{Selector: selector, Request: borg.Request{
		Args: args, Dir: invocationDir, Access: repository.Access,
	}}, nil
}

func (r Retention) args() ([]string, error) {
	var args []string
	if r.Within != "" {
		if !positiveInterval(r.Within) {
			return nil, errors.New("--keep-within must be a positive integer followed by H, d, w, m, or y")
		}
		args = append(args, "--keep-within="+r.Within)
	}
	for _, rule := range []struct {
		name  string
		count int
	}{
		{"secondly", r.Secondly}, {"minutely", r.Minutely}, {"hourly", r.Hourly},
		{"daily", r.Daily}, {"weekly", r.Weekly}, {"monthly", r.Monthly}, {"yearly", r.Yearly},
	} {
		if rule.count != 0 {
			args = append(args, "--keep-"+rule.name+"="+strconv.Itoa(rule.count))
		}
	}
	if len(args) == 0 {
		return nil, errors.New("prune requires at least one effective retention rule; zero counts alone are not a policy")
	}
	return args, nil
}

var intervalSyntax = regexp.MustCompile(`^\+?[0-9]+[Hdwmy]$`)

func positiveInterval(interval string) bool {
	if !intervalSyntax.MatchString(interval) {
		return false
	}
	// Do not impose Go's time.Duration limit or reinterpret Borg's month/year
	// lengths. Borg receives the original interval and computes the cutoff.
	number, ok := new(big.Int).SetString(interval[:len(interval)-1], 10)
	return ok && number.Sign() > 0
}
