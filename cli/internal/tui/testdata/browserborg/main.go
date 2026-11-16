package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/archive"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("borg 1.2.8")
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "list" {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "never-display-this\x1b]0;unsafe\a")
	location := os.Args[len(os.Args)-1]
	repository, name, inspect := strings.Cut(location, "::")
	encoder := json.NewEncoder(os.Stdout)
	if !inspect {
		_ = encoder.Encode(map[string]any{"archives": []archive.Archive{
			{Name: "files", ID: "1", Start: "2026-11-16"},
			{Name: "slow", ID: "2", Start: "2026-11-16"},
		}})
		return
	}
	if name == "slow" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		_ = os.WriteFile(repository+".running", []byte("running"), 0600)
		_ = encoder.Encode(archive.File{Path: "waiting", Type: "-", Mode: "-rw-------", Mtime: "2026", Healthy: true})
		<-signals
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(repository+".stopped", []byte("stopped"), 0600)
		return
	}
	for i := range 5000 {
		path := fmt.Sprintf("path/%06d", i)
		if i == 4999 {
			path = "last/needle"
		}
		_ = encoder.Encode(archive.File{Path: archive.DisplayText(path), Type: "-", Mode: "-rw-------",
			Mtime: "2026", Size: int64(i), UID: 12, GID: 34, Healthy: true})
	}
}
