package assertions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"

	"golang.org/x/sys/unix"
)

const MaxSampleFiles = 10000

type SampleStatus string

const (
	SamplePassed     SampleStatus = "passed"
	SampleDifferent  SampleStatus = "different"
	SampleIncomplete SampleStatus = "incomplete"
	SampleFailed     SampleStatus = "failed"
)

type SampleReason string

const (
	SampleEmpty             SampleReason = "empty"
	SampleOmitted           SampleReason = "omitted"
	SampleInvalidLimit      SampleReason = "invalid_limit"
	SampleRootUnavailable   SampleReason = "root_unavailable"
	SampleFilesystemFailure SampleReason = "filesystem_failure"
	SampleCanceled          SampleReason = "canceled"
)

// SampleResult contains aggregate counts only. Eligible is complete only when
// selection succeeds; Selected includes omitted files, Compared includes differences.
type SampleResult struct {
	Method     string
	Status     SampleStatus
	Reason     SampleReason
	Eligible   int64
	Selected   int
	Unselected int64
	Compared   int
	Different  int
	Omitted    int
}

type samplePath struct {
	name string
	hash [sha256.Size]byte
}

func sampleLess(a, b samplePath) bool {
	if c := bytes.Compare(a.hash[:], b.hash[:]); c != 0 {
		return c < 0
	}
	return a.name < b.name
}

// CompareSample compares a deterministic sample of restored regular paths with
// the same relative paths in live. Both roots need trusted ancestors. Neither
// tree is a snapshot; callers must keep the restored tree stable.
func CompareSample(ctx context.Context, restored, live string, limit int) SampleResult {
	r := SampleResult{Method: "sha256-relative-path-lowest", Status: SampleFailed}
	if ctx.Err() != nil {
		r.Reason = SampleCanceled
		return r
	}
	if limit <= 0 || limit > MaxSampleFiles {
		r.Reason = SampleInvalidLimit
		return r
	}
	root, err := openRoot(restored)
	if err != nil {
		r.Reason = SampleRootUnavailable
		return r
	}
	defer root.Close()
	other, err := openRoot(live)
	if err != nil {
		r.Reason = SampleRootUnavailable
		return r
	}
	defer other.Close()
	var selected []samplePath
	err = selectBelow(ctx, root, ".", "", limit, &selected, &r.Eligible)
	if err != nil {
		r.Reason = SampleFilesystemFailure
		if ctx.Err() != nil {
			r.Reason = SampleCanceled
		}
		return r
	}
	r.Selected = len(selected)
	r.Unselected = r.Eligible - int64(r.Selected)
	if r.Selected == 0 {
		r.Status = SampleIncomplete
		r.Reason = SampleEmpty
		return r
	}
	for _, p := range selected {
		if ctx.Err() != nil {
			r.Omitted = r.Selected - r.Compared
			r.Reason = SampleCanceled
			return r
		}
		equal, err := compareSampleFile(ctx, root, other, p.name)
		if err != nil {
			r.Omitted++
			if ctx.Err() != nil {
				r.Omitted = r.Selected - r.Compared
				r.Reason = SampleCanceled
				return r
			}
			if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) &&
				!errors.Is(err, errParentNotDirectory) && !errors.Is(err, errSampleNotRegular) &&
				!errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
				r.Omitted = r.Selected - r.Compared
				r.Reason = SampleFilesystemFailure
				return r
			}
			continue
		}
		r.Compared++
		if !equal {
			r.Different++
		}
	}
	switch {
	case r.Omitted > 0:
		r.Status = SampleIncomplete
		r.Reason = SampleOmitted
	case r.Different > 0:
		r.Status = SampleDifferent
	default:
		r.Status = SamplePassed
	}
	return r
}

func selectBelow(ctx context.Context, parent *os.File, name, prefix string, limit int, selected *[]samplePath, eligible *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), "sample directory")
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := dir.Readdirnames(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, child := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			rel := child
			if prefix != "" {
				rel = prefix + "/" + child
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				if err := selectBelow(ctx, dir, child, rel, limit, selected, eligible); err != nil {
					return err
				}
			case unix.S_IFREG:
				*eligible++
				p := samplePath{rel, sha256.Sum256([]byte(rel))}
				i := sort.Search(len(*selected), func(i int) bool { return !sampleLess((*selected)[i], p) })
				if i < limit {
					*selected = append(*selected, samplePath{})
					copy((*selected)[i+1:], (*selected)[i:])
					(*selected)[i] = p
					if len(*selected) > limit {
						*selected = (*selected)[:limit]
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

var errSampleNotRegular = errors.New("sample path is not a regular file")

func compareSampleFile(ctx context.Context, restored, live *os.File, name string) (bool, error) {
	a, err := lookup(ctx, restored, name)
	if err != nil {
		return false, err
	}
	defer a.file.Close()
	b, err := lookup(ctx, live, name)
	if err != nil {
		return false, err
	}
	defer b.file.Close()
	if a.kind() != 0 || b.kind() != 0 {
		return false, errSampleNotRegular
	}
	af, err := a.openRegular()
	if err != nil {
		return false, err
	}
	defer af.Close()
	bf, err := b.openRegular()
	if err != nil {
		return false, err
	}
	defer bf.Close()
	x, y := make([]byte, 32*1024), make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		nx, ex := io.ReadFull(af, x)
		ny, ey := io.ReadFull(bf, y)
		if ex != nil && ex != io.EOF && ex != io.ErrUnexpectedEOF {
			return false, ex
		}
		if ey != nil && ey != io.EOF && ey != io.ErrUnexpectedEOF {
			return false, ey
		}
		if nx != ny || !bytes.Equal(x[:nx], y[:ny]) {
			return false, nil
		}
		if ex != nil || ey != nil {
			return true, nil
		}
	}
}
