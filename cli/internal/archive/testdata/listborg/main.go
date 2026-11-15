package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

func main() {
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
	if os.Getenv("LIST_BORG_WAIT") == "yes" {
		time.Sleep(time.Minute)
	}
	code, _ := strconv.Atoi(os.Getenv("LIST_BORG_EXIT"))
	os.Exit(code)
}
