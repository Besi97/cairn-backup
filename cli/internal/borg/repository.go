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
var sshAuthority = regexp.MustCompile(`^(?:[^@:/]+@)?(?:\[[0-9a-fA-F:.]+\]|[^\[\]:/]+)(?::[0-9]+)?$`)

func repositoryLocation(location, invocationDir string) (string, bool, error) {
	ssh := strings.HasPrefix(location, "ssh://")
	path := location
	if ssh {
		authorityAndPath := strings.TrimPrefix(location, "ssh://")
		authority, remotePath, found := strings.Cut(authorityAndPath, "/")
		if !found || authority == "" || remotePath == "" {
			return "", false, errors.New("--repository SSH URL must include a host and path")
		}
		// Borg expands placeholders itself, including in the authority. Only
		// validate literal authorities here; do not parse/escape the URL with
		// net/url, which would reject native format strings such as %Y.
		if !strings.Contains(authority, "{") && !sshAuthority.MatchString(authority) {
			return "", false, errors.New("--repository SSH URL has an invalid host or port")
		}
		path = remotePath
	} else if host := scpLocation.FindString(location); host != "" && !strings.HasPrefix(location, "file://") {
		ssh = true
		path = strings.TrimPrefix(location, host)
		if path == "" {
			return "", false, errors.New("--repository SSH location must include a path")
		}
		if strings.HasPrefix(path, "//") || strings.HasPrefix(path, "ssh://") {
			return "", false, errors.New("--repository has an invalid scp-style path")
		}
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
