// Package backup prepares standalone backups without command parsing or configuration.
package backup

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

// Options contains only inputs for one backup. Source paths and patterns retain
// Borg's semantics; they are not cleaned, expanded, or checked for existence.
type Options struct {
	Repository     string
	ArchivePrefix  string
	ArchiveName    string
	SourceRoot     string
	Sources        []string
	Excludes       []string
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
	DryRun         bool
}

// Prepared identifies the requested archive template and its Borg invocation.
// Borg, not Cairn, expands any placeholders in Archive.
type Prepared struct {
	Archive string
	Request borg.Request
}

// Prepare resolves local access paths from the original invocation directory.
// Supplying the time explicitly keeps generated names deterministic for callers.
func Prepare(options Options, invocationDir string, now time.Time) (Prepared, error) {
	if options.Repository == "" {
		return Prepared{}, errors.New("--repository is required and must not be empty")
	}
	if (options.ArchivePrefix == "") == (options.ArchiveName == "") {
		return Prepared{}, errors.New("provide exactly one nonempty --archive-prefix or --archive-name")
	}
	if len(options.Sources) == 0 {
		return Prepared{}, errors.New("backup requires at least one source path")
	}
	for _, source := range options.Sources {
		if source == "" {
			return Prepared{}, errors.New("source paths must not be empty")
		}
	}
	if !filepath.IsAbs(invocationDir) {
		return Prepared{}, errors.New("invocation directory must be absolute")
	}
	lockArgs, err := borg.LockWaitArgs(options.LockWait)
	if err != nil {
		return Prepared{}, err
	}
	repository, ssh, err := repositoryLocation(options.Repository, invocationDir)
	if err != nil {
		return Prepared{}, err
	}
	if options.IdentityFile != "" && !ssh {
		return Prepared{}, errors.New("identity file requires an SSH repository")
	}
	archive := options.ArchiveName
	if options.ArchivePrefix != "" {
		archive = options.ArchivePrefix + "-" + now.UTC().Format("20060102T150405.000000000Z")
	}
	args := append([]string{"create"}, lockArgs...)
	if options.DryRun {
		args = append(args, "--dry-run", "--list")
	} else {
		args = append(args, "--stats", "--progress")
	}
	for _, exclude := range options.Excludes {
		// Bind the value to its option so patterns beginning with a hyphen
		// cannot be interpreted as another Borg option.
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, "--", repository+"::"+archive)
	args = append(args, options.Sources...)
	dir := invocationDir
	if options.SourceRoot != "" {
		dir = resolvePath(invocationDir, options.SourceRoot)
	}
	return Prepared{Archive: archive, Request: borg.Request{
		Args: args,
		Dir:  dir,
		Access: borg.AccessOptions{
			SSH:            ssh,
			PassphraseFile: resolveOptionalPath(invocationDir, options.PassphraseFile),
			IdentityFile:   resolveOptionalPath(invocationDir, options.IdentityFile),
		},
	}}, nil
}

// Borg also accepts scp-style SSH locations. Classify these before resolving
// local paths so every remote invocation receives the controlled SSH policy.
var scpLocation = regexp.MustCompile(`^(?:[^@:/]+@)?(?:\[[0-9a-fA-F:.]+\]|[^:/]+):`)

func repositoryLocation(location, invocationDir string) (string, bool, error) {
	ssh := strings.HasPrefix(location, "ssh://")
	path := location
	if ssh {
		authorityAndPath := strings.TrimPrefix(location, "ssh://")
		_, path, _ = strings.Cut(authorityAndPath, "/")
	} else if host := scpLocation.FindString(location); host != "" && !strings.HasPrefix(location, "file://") {
		ssh = true
		path = strings.TrimPrefix(location, host)
	}
	// Inspect the path, not IPv6 colons in the authority. The repository flag
	// must not carry an archive or request BORG_REPO fallback.
	if strings.Contains(path, "::") || strings.HasPrefix(path, ":") {
		return "", false, errors.New("--repository must name a repository, not an archive")
	}
	if ssh {
		return location, true, nil
	}
	if strings.HasPrefix(location, "file://") {
		path := strings.TrimPrefix(location, "file://")
		if path == "" {
			return "", false, errors.New("--repository file URL must include a path")
		}
		return "file://" + resolvePath(invocationDir, path), false, nil
	}
	return resolvePath(invocationDir, location), false, nil
}

func resolvePath(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func resolveOptionalPath(dir, path string) string {
	if path == "" {
		return ""
	}
	return resolvePath(dir, path)
}
