package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("borg 1.2.8")
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	dir := os.Getenv("HOME")
	_ = os.WriteFile(filepath.Join(dir, "running"), nil, 0600)
	sig := <-signals
	_ = os.WriteFile(filepath.Join(dir, "signal"), []byte(sig.String()), 0600)
	time.Sleep(300 * time.Millisecond)
	_ = os.WriteFile(filepath.Join(dir, "cleaned"), nil, 0600)
}
