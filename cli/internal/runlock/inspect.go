package runlock

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Evidence identifies a Cairn owner and the existing workflow coordination file,
// not a Borg process or a reported outcome.
type Evidence struct {
	LockKey    string `json:"lock_key"`
	BootID     string `json:"boot_id"`
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
}

func (e Evidence) Valid() bool {
	key := strings.TrimSuffix(strings.TrimPrefix(e.LockKey, "workflow-"), ".lock")
	raw, err := hex.DecodeString(key)
	return err == nil && len(raw) == 32 && e.LockKey == "workflow-"+key+".lock" &&
		validBootID(e.BootID) && e.PID > 0 && e.StartTicks > 0
}

func validBootID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16
}

// Capture is optional attribution. Failure must not affect execution.
func Capture(lock *os.File) *Evidence {
	if lock == nil {
		return nil
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil
	}
	ticks, _, err := process(os.Getpid())
	if err != nil {
		return nil
	}
	e := Evidence{LockKey: lock.Name(), BootID: strings.TrimSpace(string(boot)), PID: os.Getpid(), StartTicks: ticks}
	if !e.Valid() {
		return nil
	}
	return &e
}

type OwnerState string
type LockState string

const (
	OwnerUnknown OwnerState = "unknown"
	OwnerPresent OwnerState = "present"
	OwnerGone    OwnerState = "gone"
	LockUnknown  LockState  = "unknown"
	LockHeld     LockState  = "held"
)

// Observation is momentary evidence, never a run status. A held file may belong
// to surviving Borg children or a later invocation, not the recorded owner.
type Observation struct {
	Owner OwnerState
	Lock  LockState
}

type Inspector struct {
	store *Store
	boot  string
	locks []string
}

// NewInspector only opens existing coordination files. It never acquires locks,
// creates directories/files, or consults configuration. Unavailable evidence is
// represented by unknown fields rather than filesystem diagnostics.
func NewInspector() *Inspector {
	i := &Inspector{}
	i.store, _ = directories(os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("XDG_STATE_HOME"), os.Getenv("HOME"), false)
	if boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
		i.boot = strings.TrimSpace(string(boot))
	}
	if f, err := os.Open("/proc/locks"); err == nil {
		defer f.Close()
		const limit = 4 << 20
		if raw, err := io.ReadAll(io.LimitReader(f, limit+1)); err == nil && len(raw) <= limit {
			i.locks = strings.Split(string(raw), "\n")
		}
	}
	return i
}

func (i *Inspector) Close() {
	if i.store != nil {
		i.store.Close()
		i.store = nil
	}
}

func (i *Inspector) Inspect(e *Evidence) Observation {
	o := Observation{Owner: OwnerUnknown, Lock: LockUnknown}
	if e == nil || !e.Valid() {
		return o
	}
	o.Owner = observeOwner(*e, i.boot, process)
	if i.store == nil || i.locks == nil {
		return o
	}
	fd, err := syscall.Openat(int(i.store.directory.Fd()), e.LockKey,
		syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return o
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0600 || st.Nlink != 1 {
		return o
	}
	// /proc/locks uses hexadecimal device numbers and a decimal inode.
	major := (st.Dev >> 8) & 0xfff
	minor := (st.Dev & 0xff) | ((st.Dev >> 12) & 0xffffff00)
	want := fmt.Sprintf("%x:%x:%d", major, minor, st.Ino)
	for _, line := range i.locks {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 6 {
			return o
		}
		if fields[1] == "->" {
			continue
		}
		parts := strings.Split(fields[5], ":")
		if len(parts) != 3 {
			return o
		}
		a, errA := strconv.ParseUint(parts[0], 16, 64)
		b, errB := strconv.ParseUint(parts[1], 16, 64)
		inode, errC := strconv.ParseUint(parts[2], 10, 64)
		if errA != nil || errB != nil || errC != nil {
			return o
		}
		if fields[1] == "FLOCK" && fmt.Sprintf("%x:%x:%d", a, b, inode) == want {
			o.Lock = LockHeld
			return o
		}
	}
	// The kernel omits inherited flocks after their original owner disappears.
	// Absence is not evidence of release, even in the same PID namespace.
	return o
}

func observeOwner(e Evidence, boot string, lookup func(int) (uint64, string, error)) OwnerState {
	if !validBootID(boot) {
		return OwnerUnknown
	}
	if boot != e.BootID {
		return OwnerGone
	}
	ticks, state, err := lookup(e.PID)
	if os.IsNotExist(err) {
		return OwnerGone
	}
	if err != nil {
		return OwnerUnknown
	}
	if ticks != e.StartTicks || state == "Z" || state == "X" {
		return OwnerGone
	}
	return OwnerPresent
}

func process(pid int) (uint64, string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", err
	}
	// comm is parenthesized and may itself contain spaces or parentheses.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, "", fmt.Errorf("invalid process identity")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return 0, "", fmt.Errorf("invalid process identity")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	return ticks, fields[0], err
}
