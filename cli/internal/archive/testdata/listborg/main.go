package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if os.Getenv("LIST_BORG_CHILD") == "yes" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		_ = os.WriteFile(os.Getenv("LIST_BORG_READY"), []byte("ready"), 0600)
		<-signals
		fmt.Fprintln(os.Stderr, "cleanup signal")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("borg 1.2.8")
		return
	}
	fmt.Fprintln(os.Stderr, "listing diagnostic")
	if path := os.Getenv("LIST_BORG_DATA"); path != "" {
		file, err := os.Open(path)
		if err != nil {
			panic(err)
		}
		_, _ = io.Copy(os.Stdout, file)
		_ = file.Close()
	}
	if ready := os.Getenv("LIST_BORG_READY"); ready != "" {
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "LIST_BORG_CHILD=yes")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			panic(err)
		}
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	if os.Getenv("LIST_BORG_WAIT") == "yes" {
		time.Sleep(time.Minute)
	}
	code, _ := strconv.Atoi(os.Getenv("LIST_BORG_EXIT"))
	os.Exit(code)
}
