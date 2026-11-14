package borg

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// RepositoryOptions describes an explicit repository and optional access files.
// File paths resolve from Cairn's invocation directory, not Borg's working directory.
type RepositoryOptions struct {
	Repository     string
	PassphraseFile string
	IdentityFile   string
}

// PreparedRepository retains Borg's location syntax and prepared access settings.
type PreparedRepository struct {
	Location string
	Access   AccessOptions
}

// PrepareRepository resolves local paths without accessing a repository or loading
// configuration. Remote paths and placeholders retain Borg's native semantics.
func PrepareRepository(options RepositoryOptions, invocationDir string) (PreparedRepository, error) {
	if options.Repository == "" {
		return PreparedRepository{}, errors.New("--repository is required and must not be empty")
	}
	if !filepath.IsAbs(invocationDir) {
		return PreparedRepository{}, errors.New("invocation directory must be absolute")
	}
	location, ssh, err := repositoryLocation(options.Repository, invocationDir)
	if err != nil {
		return PreparedRepository{}, err
	}
	if options.IdentityFile != "" && !ssh {
		return PreparedRepository{}, errors.New("identity file requires an SSH repository")
	}
	return PreparedRepository{Location: location, Access: AccessOptions{
		SSH:            ssh,
		PassphraseFile: resolveOptionalPath(invocationDir, options.PassphraseFile),
		IdentityFile:   resolveOptionalPath(invocationDir, options.IdentityFile),
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
