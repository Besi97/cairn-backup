// Package borg locates the Borg executable and applies Cairn's supported-version
// policy before any repository work starts.
package borg

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Installation describes the Borg executable that Cairn runs.
type Installation struct {
	Path    string
	Version string
	// Warning is set when Borg is a recognizable Borg 1 version outside the
	// verified range. Execution still proceeds.
	Warning string
}

var versionLine = regexp.MustCompile(`^borg (\d+)\.(\d+)\.(\d+)(\S*)$`)

// Detect finds borg on PATH and checks its version. Stable Borg 1.4.5 and later
// below 1.5.0 is verified. Other Borg 1 versions, including prereleases, are
// reported as warnings. Major versions 2 and later, unidentifiable output, and a
// missing or non-executable borg are errors.
func Detect(ctx context.Context) (Installation, error) {
	path, err := exec.LookPath("borg")
	if err != nil {
		return Installation{}, fmt.Errorf("borg was not found or is not executable; install Borg 1.4.5 or later below 1.5.0")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	command := exec.CommandContext(ctx, path, "--version")
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		return Installation{}, fmt.Errorf("%s --version failed; check the Borg installation", path)
	}

	line := strings.TrimSpace(stdout.String())
	match := versionLine.FindStringSubmatch(line)
	if match == nil {
		return Installation{}, fmt.Errorf("%s: cannot identify the Borg version; install Borg 1.4.5 or later below 1.5.0", path)
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	version := strings.TrimPrefix(line, "borg ")
	installation := Installation{Path: path, Version: version}

	switch {
	case major != 1:
		return Installation{}, fmt.Errorf("%s: Borg %s is not supported; install Borg 1.4.5 or later below 1.5.0", path, version)
	case minor == 4 && patch >= 5 && match[4] == "":
		return installation, nil
	default:
		installation.Warning = fmt.Sprintf("Borg %s at %s is outside the verified range (1.4.5 to below 1.5.0); compatibility is not guaranteed", version, path)
		return installation, nil
	}
}
