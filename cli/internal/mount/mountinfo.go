package mount

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type entry struct {
	target, root, filesystem, source string
	uid                              string
}

func readMounts() ([]entry, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read Linux mount metadata: %w", err)
	}
	defer file.Close()
	return parseMounts(file)
}

func parseMounts(reader io.Reader) ([]entry, error) {
	var entries []entry
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) != separator+4 {
			return nil, errors.New("invalid Linux mount metadata")
		}
		root, err := unescape(fields[3])
		if err != nil {
			return nil, err
		}
		target, err := unescape(fields[4])
		if err != nil || !filepath.IsAbs(target) || filepath.Clean(target) != target {
			return nil, errors.New("invalid mountpoint in Linux mount metadata")
		}
		source, err := unescape(fields[separator+2])
		if err != nil {
			return nil, err
		}
		uid := ""
		for _, option := range strings.Split(fields[separator+3], ",") {
			if value, found := strings.CutPrefix(option, "user_id="); found {
				if uid != "" || value == "" {
					return nil, errors.New("ambiguous mounting UID in Linux mount metadata")
				}
				uid = value
			}
		}
		entries = append(entries, entry{target, root, fields[separator+1], source, uid})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Linux mount metadata: %w", err)
	}
	return entries, nil
}

func unescape(value string) (string, error) {
	var result strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			result.WriteByte(value[i])
			continue
		}
		if i+3 >= len(value) {
			return "", errors.New("invalid escape in Linux mount metadata")
		}
		var char byte
		switch value[i+1 : i+4] {
		case "040":
			char = ' '
		case "011":
			char = '\t'
		case "012":
			char = '\n'
		case "134":
			char = '\\'
		default:
			return "", errors.New("invalid escape in Linux mount metadata")
		}
		result.WriteByte(char)
		i += 3
	}
	return result.String(), nil
}

func atTarget(entries []entry, target string) []entry {
	var matches []entry
	for _, mounted := range entries {
		if mounted.target == target {
			matches = append(matches, mounted)
		}
	}
	return matches
}

func verify(entries []entry, target string, uid int) error {
	matches := atTarget(entries, target)
	if len(matches) != 1 {
		return errors.New("unmount requires exactly one mount at the exact mountpoint")
	}
	mounted := matches[0]
	if mounted.root != "/" || mounted.source != "borgfs" ||
		(mounted.filesystem != "fuse" && mounted.filesystem != "fuse.borgfs") ||
		mounted.uid != strconv.Itoa(uid) {
		return errors.New("unmount requires a verified same-user Borg FUSE mount")
	}
	return nil
}
