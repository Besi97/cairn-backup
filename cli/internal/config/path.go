package config

import (
	"errors"
	"strings"
)

// LocationKind distinguishes a local repository path from an SSH location.
type LocationKind int

const (
	// LocationLocal is a filesystem path.
	LocationLocal LocationKind = iota
	// LocationSSH is a Borg SSH location, for example ssh://user@host/path or
	// user@host:path.
	LocationSSH
)

// ClassifyLocation reports whether a repository location is a local path or an
// SSH location. SSH locations are never expanded or resolved to a local path.
func ClassifyLocation(location string) LocationKind {
	if strings.HasPrefix(location, "ssh://") {
		return LocationSSH
	}

	// Borg also accepts the scp-like user@host:path form. Treat a colon in the
	// first segment as a remote location unless that segment looks like a path.
	if index := strings.IndexByte(location, ':'); index > 0 && !strings.Contains(location[:index], "/") {
		return LocationSSH
	}
	return LocationLocal
}

// ExpandPath expands a leading ~/ using the environment's home directory and
// $VAR or ${VAR} using its environment. The result is not resolved against the
// configuration directory; that happens during loading. Command substitution
// is rejected rather than passed through.
func ExpandPath(value string, env Environment) (string, error) {
	if value == "" {
		return "", nil
	}

	var builder strings.Builder
	for i := 0; i < len(value); {
		switch {
		case value[i] == '~' && i == 0 && (len(value) == 1 || value[i+1] == '/'):
			home, err := env.homeDir()
			if err != nil {
				return "", errors.New("expand path: cannot determine home directory")
			}
			if home == "" {
				return "", errors.New("expand path: home directory is not set")
			}
			builder.WriteString(home)
			i++

		case value[i] == '$' && i+1 < len(value) && value[i+1] == '(':
			return "", errors.New("expand path: command substitution is not supported")

		case value[i] == '$' && i+1 < len(value) && value[i+1] == '{':
			end := strings.IndexByte(value[i+2:], '}')
			if end < 0 {
				return "", errors.New("expand path: unterminated variable reference")
			}
			name := value[i+2 : i+2+end]
			if !validName(name) {
				return "", errors.New("expand path: invalid variable name")
			}
			expanded, err := expandVariable(name, env)
			if err != nil {
				return "", err
			}
			builder.WriteString(expanded)
			i += 2 + end + 1

		case value[i] == '$' && i+1 < len(value) && isNameStart(value[i+1]):
			end := i + 1
			for end < len(value) && isNameChar(value[end]) {
				end++
			}
			name := value[i+1 : end]
			expanded, err := expandVariable(name, env)
			if err != nil {
				return "", err
			}
			builder.WriteString(expanded)
			i = end

		default:
			builder.WriteByte(value[i])
			i++
		}
	}
	return builder.String(), nil
}

func expandVariable(name string, env Environment) (string, error) {
	variable := env.getenv(name)
	if variable == "" {
		return "", errors.New("expand path: referenced variable is unset or empty")
	}
	return variable, nil
}
