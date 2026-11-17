package restore

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

type entry struct {
	kind byte
	link string
}

func validPath(name string, root bool) error {
	if name == "." && root {
		return nil
	}
	if name == "" || len(name) > 1<<20 || strings.ContainsRune(name, 0) || path.IsAbs(name) || path.Clean(name) != name || name == "." {
		return errors.New("must be a canonical relative archive path, without empty, . or .. components")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return errors.New("parent traversal is forbidden")
		}
	}
	return nil
}

func readListing(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	scanner.Split(splitNUL)
	field := func() (string, error) {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", err
			}
			return "", io.ErrUnexpectedEOF
		}
		return scanner.Text(), nil
	}
	var kinds []byte
	for scanner.Scan() {
		kind := scanner.Text()
		mode, err := field()
		if err != nil {
			return nil, err
		}
		if len(kind) != 1 || !strings.ContainsAny(kind, "-dlpcb") || len(mode) != 10 ||
			(mode[0] != kind[0] && !(mode[0] == 'h' && kind == "-")) {
			return nil, errors.New("unsupported or malformed file type in native listing")
		}
		kinds = append(kinds, kind[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return kinds, nil
}

func splitNUL(data []byte, atEOF bool) (int, []byte, error) {
	if index := strings.IndexByte(string(data), 0); index >= 0 {
		return index + 1, data[:index], nil
	}
	if atEOF && len(data) != 0 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	return 0, nil, nil
}

func checkParents(name string, entries map[string]*entry) error {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if e := entries[parent]; e != nil && e.kind != 'd' {
			return fmt.Errorf("archive path %q traverses non-directory %q", name, parent)
		}
	}
	return nil
}

func checkDestination(destination, name string) error {
	current := destination
	for _, component := range strings.Split(name, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect destination path %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path %q encounters a symlink", name)
		}
		if current != filepath.Join(destination, name) && !info.IsDir() {
			return fmt.Errorf("destination parent for %q is not a directory", name)
		}
		if info.Mode().IsRegular() && info.Sys().(*syscall.Stat_t).Nlink > 1 {
			return fmt.Errorf("destination path %q has existing hardlinks; use a different destination", name)
		}
	}
	return nil
}

func checkTar(r io.Reader, destination string, kinds []byte) error {
	tr := tar.NewReader(r)
	entries := make(map[string]*entry)
	index := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := header.Name
		if header.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if err := validPath(name, header.Typeflag == tar.TypeDir); err != nil {
			return fmt.Errorf("unsafe exported path %q: %w", name, err)
		}
		if entries[name] != nil {
			return fmt.Errorf("duplicate archive path %q", name)
		}
		kind := byte(0)
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeLink:
			kind = '-'
		case tar.TypeDir:
			kind = 'd'
		case tar.TypeSymlink:
			kind = 'l'
		case tar.TypeFifo:
			kind = 'p'
		case tar.TypeChar:
			kind = 'c'
		case tar.TypeBlock:
			kind = 'b'
		}
		if index >= len(kinds) || kind != kinds[index] {
			return fmt.Errorf("exported file type for %q differs from the native listing", name)
		}
		index++
		e := &entry{kind: kind}
		entries[name] = e
		if err := checkDestination(destination, name); err != nil {
			return err
		}
		if header.Typeflag == tar.TypeLink {
			if err := validPath(header.Linkname, false); err != nil {
				return fmt.Errorf("unsafe hardlink target for %q: %w", name, err)
			}
			e.link = header.Linkname
			if err := checkDestination(destination, header.Linkname); err != nil {
				return err
			}
		}
	}
	if index != len(kinds) {
		return errors.New("preflight export is missing native archive entries")
	}
	for name, e := range entries {
		if err := checkParents(name, entries); err != nil {
			return err
		}
		if e.link != "" {
			if err := checkParents(e.link, entries); err != nil {
				return err
			}
			if target := entries[e.link]; target == nil || target.kind != '-' || e.link == name {
				return fmt.Errorf("hardlink %q does not target another selected regular file", name)
			}
		}
	}
	// tar EOF precedes Borg's padding and process exit; keep stdout draining.
	_, err := io.Copy(io.Discard, r)
	return err
}

func stream(ctx context.Context, runner borg.Runner, request borg.Request, consume func(io.Reader) error) (borg.Result, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := consume(reader)
		_ = reader.CloseWithError(err)
		if err != nil {
			cancel()
		}
		done <- err
	}()
	request.Stdout = writer
	result, runErr := runner.Run(runCtx, request)
	_ = writer.Close()
	decodeErr := <-done
	if ctx.Err() != nil {
		result.Outcome = borg.Interrupted
		return result, errors.Join(ctx.Err(), runErr)
	}
	if runErr != nil {
		if decodeErr == nil || errors.Is(decodeErr, io.ErrUnexpectedEOF) {
			return result, runErr
		}
	}
	if decodeErr != nil {
		result.Outcome = borg.Failure
		return result, fmt.Errorf("restore preflight: %w", errors.Join(decodeErr, runErr))
	}
	return result, runErr
}
