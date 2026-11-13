// Fake Borg used by runner tests. It is compiled once, not invoked through a shell.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if filepath.Base(os.Args[0]) == "ssh" {
		data, _ := json.Marshal(os.Args[1:])
		if err := os.WriteFile(os.Getenv("FAKE_SSH_RECORD"), data, 0600); err != nil {
			panic(err)
		}
		os.Exit(2)
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if path := os.Getenv("FAKE_CREDENTIAL_PATH"); path != "" {
			fds, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				panic(err)
			}
			for _, fd := range fds {
				target, _ := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
				if target == path {
					os.Exit(2)
				}
			}
		}
		fmt.Println(os.Getenv("FAKE_VERSION"))
		if os.Getenv("FAKE_REMOVE_AFTER_PROBE") == "yes" {
			_ = os.Remove(os.Args[0])
		}
		code, _ := strconv.Atoi(os.Getenv("FAKE_VERSION_EXIT"))
		os.Exit(code)
	}
	if os.Getenv("FAKE_READ_PASSPHRASE") == "yes" {
		fd, err := strconv.Atoi(os.Getenv("BORG_PASSPHRASE_FD"))
		if err != nil {
			panic("missing passphrase descriptor")
		}
		file := os.NewFile(uintptr(fd), "passphrase")
		data, err := io.ReadAll(file)
		if err != nil {
			panic("unreadable passphrase descriptor")
		}
		_ = file.Close()
		fmt.Fprintf(os.Stdout, "passphrase digest: %x\n", sha256.Sum256(data))
	}
	if path := os.Getenv("FAKE_RECORD"); path != "" {
		dir, _ := os.Getwd()
		data, _ := json.Marshal(struct {
			Args  []string
			Dir   string
			Value string
		}{os.Args[1:], dir, os.Getenv("FAKE_VALUE")})
		if err := os.WriteFile(path, data, 0600); err != nil {
			panic(err)
		}
	}
	fmt.Fprint(os.Stdout, "stdout diagnostic\n")
	fmt.Fprint(os.Stderr, "stderr diagnostic\n")
	if release := os.Getenv("FAKE_RELEASE"); release != "" {
		deadline := time.After(10 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			select {
			case <-deadline:
				os.Exit(2)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	if os.Getenv("FAKE_SIGNAL") == "yes" {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {}
	}
	code, _ := strconv.Atoi(os.Getenv("FAKE_EXIT"))
	os.Exit(code)
}
