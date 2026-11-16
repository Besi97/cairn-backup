// Package restore prepares and executes manual, native Borg extraction.
package restore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

type Options struct {
	Repository     string
	ArchiveName    string
	Destination    string
	Paths          []string
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
	DryRun         bool
}

// Plan retains byte-exact caller input, never archive.DisplayText. Repository and
// destination must be trusted and unchanged by other processes throughout Run.
type Plan struct {
	request     borg.Request
	destination string
	location    string
	selectors   []string
	dryRun      bool
}

func Prepare(options Options, invocationDir string) (Plan, error) {
	repository, err := borg.PrepareRepository(borg.RepositoryOptions{
		Repository: options.Repository, PassphraseFile: options.PassphraseFile, IdentityFile: options.IdentityFile,
	}, invocationDir)
	if err != nil {
		return Plan{}, err
	}
	if options.ArchiveName == "" || strings.ContainsAny(options.ArchiveName, "/\x00") ||
		strings.Contains(options.ArchiveName, "::") || strings.ContainsAny(options.ArchiveName, "{}") {
		return Plan{}, errors.New("--archive-name must be a nonempty literal archive name without /, NUL, ::, or placeholders")
	}
	if strings.ContainsAny(repository.Location, "{}") {
		return Plan{}, errors.New("restore requires a literal repository location without placeholders")
	}
	if options.Destination == "" || strings.ContainsRune(options.Destination, 0) {
		return Plan{}, errors.New("--destination is required and must not be empty or contain NUL")
	}
	destination := options.Destination
	if !filepath.IsAbs(destination) {
		destination = invocationDir + "/" + destination
	}
	destination = strings.TrimRight(destination, "/")
	if destination == "" {
		destination = "/"
	}
	if _, err := directoryChain(destination); err != nil {
		return Plan{}, fmt.Errorf("destination: %w", err)
	}
	destination = filepath.Clean(destination)
	if destination == "/" {
		return Plan{}, errors.New("destination must not be the filesystem root")
	}
	if _, err := directoryChain(filepath.Dir(destination)); err != nil {
		return Plan{}, fmt.Errorf("destination parent: %w", err)
	}
	if info, err := os.Stat(filepath.Dir(destination)); err != nil || !info.IsDir() {
		return Plan{}, errors.New("destination parent must be an existing directory")
	}
	if !repository.Access.SSH {
		local := strings.TrimPrefix(repository.Location, "file://")
		local, err = filepath.EvalSymlinks(local)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Plan{}, fmt.Errorf("resolve local repository: %w", err)
		}
		if local == "" {
			local = strings.TrimPrefix(repository.Location, "file://")
		}
		if beneath(destination, local) || beneath(local, destination) {
			return Plan{}, errors.New("destination and local repository must not overlap")
		}
	}
	lockArgs, err := borg.LockWaitArgs(options.LockWait)
	if err != nil {
		return Plan{}, err
	}
	selectors := make([]string, 0, len(options.Paths))
	for _, name := range options.Paths {
		if err := validPath(name, false); err != nil {
			return Plan{}, fmt.Errorf("restore path %q: %w", name, err)
		}
		selectors = append(selectors, "pp:"+name)
	}
	return Plan{
		request:     borg.Request{Args: lockArgs, Dir: invocationDir, Access: repository.Access},
		destination: destination, location: repository.Location + "::" + options.ArchiveName,
		selectors: selectors, dryRun: options.DryRun,
	}, nil
}

func (p Plan) Destination() string { return p.destination }

func (p Plan) Existing() (bool, error) {
	if p.destination == "" {
		return false, errors.New("restore plan must be prepared before use")
	}
	return directoryChain(p.destination)
}

func (p Plan) command(operation string, flags ...string) borg.Request {
	request := p.request
	request.Args = append([]string{operation}, request.Args...)
	request.Args = append(request.Args, flags...)
	request.Args = append(request.Args, "--", p.location)
	if operation == "export-tar" {
		request.Args = append(request.Args, "-")
	}
	request.Args = append(request.Args, p.selectors...)
	return request
}

// Run preflights byte-preserving metadata before native extraction. allowExisting
// means the caller has explicitly authorized replacement, not just directory use.
// This is not a sandbox, transaction, or protection against concurrent mutation.
func (p Plan) Run(ctx context.Context, runner borg.Runner, allowExisting bool, diagnostics io.Writer) (borg.Result, error) {
	failed := borg.Result{Outcome: borg.Failure, ExitCode: -1}
	existing, err := p.Existing()
	if err != nil {
		return failed, err
	}
	if existing && !p.dryRun && !allowExisting {
		return failed, errors.New("existing destination requires explicit replacement authorization")
	}
	access, closeAccess, err := borg.ReusableAccess(ctx, p.request.Access)
	if err != nil {
		if ctx.Err() != nil {
			failed.Outcome = borg.Interrupted
		}
		return failed, err
	}
	defer closeAccess()
	p.request.Access = access
	var kinds []byte
	request := p.command("list", "--format", "{type}{NUL}{mode}{NUL}")
	request.Diagnostics = diagnostics
	result, err := stream(ctx, runner, request, func(r io.Reader) error {
		var err error
		kinds, err = readListing(r)
		return err
	})
	if result.Outcome == borg.Warning && err == nil {
		err = errors.New("restore preflight reported warnings; extraction was not attempted")
	}
	if err != nil || result.Outcome != borg.Success {
		return result, err
	}
	request = p.command("export-tar")
	request.Diagnostics = diagnostics
	result, err = stream(ctx, runner, request, func(r io.Reader) error {
		return checkTar(r, p.destination, kinds)
	})
	if result.Outcome == borg.Warning && err == nil {
		err = errors.New("restore preflight reported warnings; extraction was not attempted")
	}
	if err != nil || result.Outcome != borg.Success {
		return result, err
	}
	if ctx.Err() != nil {
		return borg.Result{Outcome: borg.Interrupted, ExitCode: -1}, ctx.Err()
	}
	request = p.command("extract", "--list")
	request.Diagnostics = diagnostics
	if p.dryRun {
		// Borg dry-run needs a cwd, but must not create the proposed destination.
		request.Args = append([]string{"extract", "--dry-run"}, request.Args[1:]...)
		return runner.Run(ctx, request)
	}
	existing, err = p.Existing()
	if err != nil {
		return failed, err
	}
	if existing && !allowExisting {
		return failed, errors.New("destination appeared after preparation; replacement was not authorized")
	}
	if !existing {
		if err := os.Mkdir(p.destination, 0700); err != nil {
			return failed, fmt.Errorf("create destination: %w", err)
		}
	}
	request.Dir = p.destination
	return runner.Run(ctx, request)
}

func beneath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// Check components before cleaning, so link/../destination cannot hide a link.
func directoryChain(name string) (bool, error) {
	current := "/"
	parts := strings.Split(name, "/")
	for i, component := range parts {
		if component == "" {
			continue
		}
		current += "/" + component
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && i == len(parts)-1 {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() {
			return false, fmt.Errorf("%q is not a real directory (symlinks are forbidden)", current)
		}
	}
	return true, nil
}
