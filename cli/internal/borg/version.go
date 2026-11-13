package borg

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var versionPattern = regexp.MustCompile(`^borg ([0-9]+)\.([0-9]+)\.([0-9]+)$`)

func supportedVersion(output string) error {
	parts := versionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if parts == nil {
		return errors.New("unrecognized Borg version; require stable Borg 1.x, minimum 1.2.8")
	}
	var version [3]int
	for i := range version {
		n, err := strconv.Atoi(parts[i+1])
		if err != nil {
			return errors.New("unrecognized Borg version; require stable Borg 1.x, minimum 1.2.8")
		}
		version[i] = n
	}
	if version[0] != 1 || version[1] < 2 || version[1] == 2 && version[2] < 8 {
		return errors.New("unsupported Borg version; require stable Borg 1.x, minimum 1.2.8")
	}
	return nil
}
