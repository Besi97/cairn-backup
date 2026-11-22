// Package drill orchestrates an internal recovery attempt.
package drill

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/assertions"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/selection"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/workspace"
)

type SampleOptions struct {
	LiveRoot string
	Limit    int
}

type Options struct {
	Repository   borg.PreparedRepository
	Dir          string
	LockWait     *int
	Selector     selection.Selector
	Paths        []string
	WholeArchive bool
	Checks       []assertions.Check
	Sample       *SampleOptions
}

// Result keeps the integrity check separate because report.Evidence has no field
// for it. Warning indicates differences or incomplete sampling, not success.
type Result struct {
	BorgCheck report.Step
	Evidence  report.Evidence
	Warning   bool
}

// Run performs a whole-repository Borg metadata check without --verify-data,
// then selects and extracts one archive. The caller owns repository-level
// coordination and must protect diagnostics and full evidence as sensitive.
func Run(ctx context.Context, runner borg.Runner, o Options, diagnostics io.Writer) (out Result, err error) {
	start := time.Now()
	notRun := report.Step{Status: report.NotRun}
	out.BorgCheck = notRun
	out.Evidence = report.Evidence{
		Outcome: notRun, Selection: notRun, IdentityBefore: notRun,
		Extraction: notRun, IdentityAfter: notRun, ChecksState: notRun,
		Cleanup: notRun, Sample: report.Sample{Status: "not_run"},
		Timing: report.Timing{Started: &start},
	}
	e := &out.Evidence
	defer func() {
		finish := time.Now()
		e.Timing.Finished = &finish
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
			e.Outcome = report.Step{Status: report.Interrupted, Reason: "canceled"}
		} else if err != nil {
			if e.Outcome.Status == report.NotRun {
				e.Outcome = report.Step{Status: report.Failed}
			}
		} else if out.Warning {
			e.Outcome = report.Step{Status: report.Incomplete}
		} else {
			e.Outcome = report.Step{Status: report.Passed}
		}
	}()

	// The renderer has strict bounds and codes. Reject unsupported inputs before
	// starting Borg so every returned report remains renderable.
	if len(o.Paths) > 1000 || len(o.Checks) > 1000 {
		return out, errors.New("recovery evidence exceeds item limit")
	}
	for _, p := range o.Paths {
		if len(p) > 4096 {
			return out, errors.New("recovery path exceeds evidence limit")
		}
	}
	for _, c := range o.Checks {
		if len(c.Path) > 4096 || !validKind(c.Kind) {
			return out, errors.New("recovery assertion cannot be represented in evidence")
		}
		e.Checks = append(e.Checks, report.Check{Kind: string(c.Kind), Path: c.Path, Status: string(assertions.NotEvaluated)})
	}
	e.Scope = report.Scope{WholeArchive: o.WholeArchive, Paths: append([]string(nil), o.Paths...)}
	if !filepath.IsAbs(o.Dir) || o.Repository.Location == "" || strings.HasPrefix(o.Repository.Location, ":") || strings.ContainsAny(o.Repository.Location, "{}\x00") {
		return out, errors.New("recovery requires a prepared literal repository and absolute working directory")
	}
	lockArgs, lockErr := borg.LockWaitArgs(o.LockWait)
	if lockErr != nil {
		return out, lockErr
	}
	args := append([]string{"check"}, lockArgs...)
	args = append(args, "--", o.Repository.Location)
	borgResult, checkErr := runner.Run(ctx, borg.Request{Args: args, Dir: o.Dir, Access: o.Repository.Access, Diagnostics: diagnostics})
	if checkErr != nil || borgResult.Outcome != borg.Success {
		out.BorgCheck = failed(ctx, "checks_failed")
		e.Outcome = out.BorgCheck
		return out, errors.Join(errors.New("Borg repository check did not succeed"), checkErr)
	}
	out.BorgCheck = report.Step{Status: report.Passed}
	opts := selection.Options{Repository: o.Repository, Dir: o.Dir, LockWait: o.LockWait}
	candidate, _, selectErr := selection.Resolve(ctx, runner, opts, o.Selector, diagnostics)
	if selectErr != nil {
		e.Selection = failed(ctx, "selection_failed")
		e.Outcome = e.Selection
		return out, selectErr
	}
	if len(candidate.Name) > 4096 {
		e.Selection = failed(ctx, "selection_failed")
		e.Outcome = e.Selection
		return out, errors.New("archive name exceeds evidence limit")
	}
	e.Selection = report.Step{Status: report.Passed}
	e.Archive = &report.Archive{Name: candidate.Name, ArchiveID: candidate.ArchiveID, RepositoryID: candidate.RepositoryID}
	if _, err = selection.Revalidate(ctx, runner, opts, candidate, diagnostics); err != nil {
		e.IdentityBefore = identityFailure(ctx, err)
		e.Outcome = e.IdentityBefore
		return out, err
	}
	e.IdentityBefore = report.Step{Status: report.Passed}
	callbackRan := false
	borgResult, cleaned, runErr := workspace.RunWithCleanup(ctx, runner, workspace.Options{
		Repository: o.Repository, ArchiveName: candidate.Name, Paths: o.Paths,
		WholeArchive: o.WholeArchive, LockWait: o.LockWait,
	}, diagnostics, func(ctx context.Context, root string) error {
		callbackRan = true
		e.Extraction = report.Step{Status: report.Passed}
		if _, err := selection.Revalidate(ctx, runner, opts, candidate, diagnostics); err != nil {
			e.IdentityAfter = identityFailure(ctx, err)
			e.Outcome = e.IdentityAfter
			return err
		}
		e.IdentityAfter = report.Step{Status: report.Passed}
		results := assertions.Evaluate(ctx, root, o.Checks)
		e.ChecksState = report.Step{Status: report.Passed}
		checksFailed := false
		for i, r := range results {
			e.Checks[i].Status, e.Checks[i].Reason = string(r.Status), string(r.Reason)
			checksFailed = checksFailed || r.Status != assertions.Passed
		}
		if checksFailed {
			e.ChecksState = failed(ctx, "checks_failed")
		}
		if ctx.Err() != nil {
			e.Outcome = e.ChecksState
			return ctx.Err()
		}
		if o.Sample != nil {
			s := assertions.CompareSample(ctx, root, o.Sample.LiveRoot, o.Sample.Limit)
			e.Sample = report.Sample{Status: string(s.Status), Reason: string(s.Reason), Method: s.Method,
				EligibleComplete: s.Status != assertions.SampleFailed, Eligible: s.Eligible, Selected: s.Selected,
				Unselected: s.Unselected, Compared: s.Compared, Different: s.Different, Omitted: s.Omitted}
			if s.Status == assertions.SampleFailed || s.Reason == assertions.SampleOmitted {
				e.Outcome = failed(ctx, "sample_failed")
				return errors.New("sample comparison failed")
			}
			out.Warning = s.Status != assertions.SamplePassed
		}
		if checksFailed {
			e.Outcome = e.ChecksState
			return errors.New("recovery assertions failed")
		}
		return nil
	})
	if cleaned {
		e.Cleanup = report.Step{Status: report.Passed}
	}
	var cleanupErr *workspace.CleanupError
	if errors.As(runErr, &cleanupErr) {
		reason := "cleanup_failed"
		if errors.Is(runErr, borg.ErrProcessGroupNotStopped) {
			reason = "shutdown_unconfirmed"
		}
		e.Cleanup = report.Step{Status: report.Failed, Reason: reason}
		e.Outcome = e.Cleanup
	}
	if !callbackRan {
		reason := "extraction_failed"
		if borgResult.Outcome == borg.Warning {
			reason = "extraction_warning"
		}
		e.Extraction = failed(ctx, reason)
		if e.Outcome.Status == report.NotRun {
			e.Outcome = e.Extraction
		}
	}
	return out, runErr
}

func validKind(k assertions.Kind) bool {
	switch k {
	case assertions.PathType, assertions.FileSize, assertions.FileBytes, assertions.FileCount, assertions.FileSHA256:
		return true
	}
	return false
}

func failed(ctx context.Context, reason string) report.Step {
	if ctx.Err() != nil {
		return report.Step{Status: report.Interrupted, Reason: "canceled"}
	}
	return report.Step{Status: report.Failed, Reason: reason}
}

func identityFailure(ctx context.Context, err error) report.Step {
	reason := "identity_check_failed"
	if errors.Is(err, selection.ErrChanged) || errors.Is(err, selection.ErrNotFound) {
		reason = "identity_changed"
	}
	return failed(ctx, reason)
}
