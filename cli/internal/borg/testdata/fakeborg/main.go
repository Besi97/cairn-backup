// Fake Borg used by runner tests. It is compiled once, not invoked through a shell.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(os.Getenv("FAKE_VERSION"))
		if os.Getenv("FAKE_REMOVE_AFTER_PROBE") == "yes" {
			_ = os.Remove(os.Args[0])
		}
		code, _ := strconv.Atoi(os.Getenv("FAKE_VERSION_EXIT"))
		os.Exit(code)
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
