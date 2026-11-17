package borg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReusableAccess(t *testing.T) {
	isolateCredentials(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	const secret = "credential never placed in environment"
	if _, err := writer.WriteString(secret); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	t.Setenv("BORG_PASSPHRASE_FD", strconv.Itoa(int(reader.Fd())))
	access, closeAccess, err := ReusableAccess(t.Context(), AccessOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAccess()
	if access.Passphrase != nil || !strings.HasPrefix(access.PassphraseFile, "/proc/self/fd/") {
		t.Fatalf("access=%+v", access)
	}
	for range 3 {
		data, err := os.ReadFile(access.PassphraseFile)
		if err != nil || string(data) != secret {
			t.Fatalf("credential did not rewind: bytes=%d error=%v", len(data), err)
		}
	}
	file, err := os.OpenFile(access.PassphraseFile, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("tampered"); err == nil {
		t.Fatal("snapshot was not sealed")
	}
	if _, err := reader.Stat(); err != nil {
		t.Fatal("caller descriptor closed")
	}
	flags, err := unix.FcntlInt(reader.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatal("original descriptor could leak")
	}
	closeAccess()
	if _, err := os.Open(access.PassphraseFile); err == nil {
		t.Fatal("snapshot descriptor survived cleanup")
	}
}

func TestReusableAccessPrecedence(t *testing.T) {
	isolateCredentials(t)
	t.Setenv("BORG_PASSPHRASE_FD", "invalid")
	for _, key := range []string{"BORG_PASSPHRASE", "BORG_PASSCOMMAND"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "")
			options, closeAccess, err := ReusableAccess(t.Context(), AccessOptions{})
			defer closeAccess()
			if err != nil || options.PassphraseFile != "" {
				t.Fatalf("precedence changed: %+v %v", options, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(path, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	access, closeAccess, err := ReusableAccess(t.Context(), AccessOptions{PassphraseFile: path})
	defer closeAccess()
	if err != nil || access.PassphraseFile != path {
		t.Fatal("explicit credential file lost precedence")
	}
	if _, closeAccess, err := ReusableAccess(t.Context(), AccessOptions{}); err == nil {
		closeAccess()
		t.Fatal("invalid inherited descriptor accepted")
	}
}

func TestReusableAccessCancellationAndLimit(t *testing.T) {
	isolateCredentials(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	t.Setenv("BORG_PASSPHRASE_FD", strconv.Itoa(int(reader.Fd())))
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, closeAccess, err := ReusableAccess(ctx, AccessOptions{})
	closeAccess()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read cancellation=%v", err)
	}
	path := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	t.Setenv("BORG_PASSPHRASE_FD", strconv.Itoa(int(file.Fd())))
	_, closeAccess, err = ReusableAccess(t.Context(), AccessOptions{})
	closeAccess()
	if err == nil || strings.Contains(err.Error(), "xxx") {
		t.Fatal("oversized credential did not fail safely")
	}
}
