package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
)

// ReadSecret reads a single-line secret from a file. One trailing line break
// (LF or CRLF) is removed; spaces, tabs, and other characters are preserved.
// Empty files, blank secrets, extra lines, and NUL bytes are rejected. Errors
// never include the file's contents.
func ReadSecret(path string) (string, error) {
	if path == "" {
		return "", errors.New("secret file path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("secret file %s: contains a NUL byte", path)
	}

	secret := string(data)
	if len(secret) > 0 && secret[len(secret)-1] == '\n' {
		secret = secret[:len(secret)-1]
		if len(secret) > 0 && secret[len(secret)-1] == '\r' {
			secret = secret[:len(secret)-1]
		}
	}
	if secret == "" {
		return "", fmt.Errorf("secret file %s: is empty", path)
	}
	if bytes.ContainsAny([]byte(secret), "\r\n") {
		return "", fmt.Errorf("secret file %s: must contain a single line", path)
	}
	return secret, nil
}
