// Package assertions checks fixed expectations against an extracted tree.
package assertions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
)

type Kind string

const (
	PathType   Kind = "path_type"
	FileSize   Kind = "file_size"
	FileBytes  Kind = "file_bytes"
	FileCount  Kind = "file_count"
	FileSHA256 Kind = "file_sha256"
)

// Check uses Path and the expectation selected by Kind. Type is a file type
// without permission bits (zero for regular files). Bytes may be nil for empty
// files. Size is in bytes, Count counts regular paths, and Digest is binary.
type Check struct {
	Kind   Kind
	Path   string
	Type   fs.FileMode
	Size   int64
	Bytes  []byte
	Count  int64
	Digest [sha256.Size]byte
}

type Status string

const (
	Passed           Status = "passed"
	Mismatch         Status = "mismatch"
	EvaluationFailed Status = "evaluation_failed"
	NotEvaluated     Status = "not_evaluated"
)

type Reason string

const (
	MissingPath        Reason = "missing_path"
	WrongType          Reason = "wrong_type"
	DifferentValue     Reason = "different_value"
	InvalidCheck       Reason = "invalid_check"
	RootUnavailable    Reason = "root_unavailable"
	ParentNotDirectory Reason = "parent_not_directory"
	FilesystemFailure  Reason = "filesystem_failure"
	Canceled           Reason = "canceled"
)

// Result contains only fixed codes, never paths, contents, digests or OS errors.
type Result struct {
	Status Status
	Reason Reason
}

// Evaluate returns one result per check, in input order. The caller supplies
// a stable extracted tree at root, with trusted ancestors of root.
func Evaluate(ctx context.Context, root string, checks []Check) []Result {
	results := make([]Result, len(checks))
	if len(checks) == 0 {
		return results
	}

	var tree *os.File
	var rootErr error
	for i, check := range checks {
		switch {
		case ctx.Err() != nil:
			results[i] = Result{NotEvaluated, Canceled}
		case !check.valid():
			results[i] = Result{EvaluationFailed, InvalidCheck}
		default:
			if tree == nil && rootErr == nil {
				tree, rootErr = openRoot(root)
				if tree != nil {
					defer tree.Close()
				}
			}
			if rootErr != nil {
				results[i] = Result{EvaluationFailed, RootUnavailable}
			} else {
				results[i] = check.evaluate(ctx, tree)
			}
			if ctx.Err() != nil {
				results[i] = Result{EvaluationFailed, Canceled}
			}
		}
	}
	return results
}

func (c Check) valid() bool {
	switch c.Kind {
	case PathType:
		switch c.Type {
		case 0, fs.ModeDir, fs.ModeSymlink, fs.ModeNamedPipe, fs.ModeSocket,
			fs.ModeDevice, fs.ModeDevice | fs.ModeCharDevice:
			return true
		}
	case FileSize:
		return c.Size >= 0
	case FileCount:
		return c.Count >= 0
	case FileBytes, FileSHA256:
		return true
	}
	return false
}

func (c Check) evaluate(ctx context.Context, tree *os.File) Result {
	n, err := lookup(ctx, tree, c.Path)
	switch {
	case errors.Is(err, errInvalidPath):
		return Result{EvaluationFailed, InvalidCheck}
	case errors.Is(err, fs.ErrNotExist):
		return Result{Mismatch, MissingPath}
	case errors.Is(err, errParentNotDirectory):
		return Result{EvaluationFailed, ParentNotDirectory}
	case err != nil:
		return Result{EvaluationFailed, FilesystemFailure}
	}
	defer n.file.Close()

	expectedType := fs.FileMode(0)
	switch c.Kind {
	case PathType:
		expectedType = c.Type
	case FileCount:
		expectedType = fs.ModeDir
	}
	if n.kind() != expectedType {
		return Result{Mismatch, WrongType}
	}

	var equal bool
	switch c.Kind {
	case PathType:
		equal = true
	case FileSize:
		equal = n.stat.Size == c.Size
	case FileCount:
		var count int64
		count, err = countRegular(ctx, n.file)
		equal = count == c.Count
	case FileBytes, FileSHA256:
		equal, err = c.compareContent(ctx, n)
	}
	switch {
	case err != nil:
		return Result{EvaluationFailed, FilesystemFailure}
	case !equal:
		return Result{Mismatch, DifferentValue}
	default:
		return Result{Status: Passed}
	}
}

func (c Check) compareContent(ctx context.Context, n node) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f, err := n.openRegular()
	if err != nil {
		return false, err
	}
	defer f.Close()

	digest := sha256.New()
	want := c.Bytes
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		size, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return false, err
		}
		chunk := buf[:size]
		if c.Kind == FileBytes {
			if len(want) < size || !bytes.Equal(chunk, want[:size]) {
				return false, nil
			}
			want = want[size:]
		} else {
			_, _ = digest.Write(chunk)
		}
		if err == io.EOF {
			if c.Kind == FileBytes {
				return len(want) == 0, nil
			}
			return bytes.Equal(digest.Sum(nil), c.Digest[:]), nil
		}
	}
}
