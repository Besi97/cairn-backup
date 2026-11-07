package main

import (
	"flag"
	"fmt"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("cairn %s\n", version)
		return
	}

	fmt.Fprintln(os.Stderr, "cairn: Borg backup scheduler (not yet implemented)")
	fmt.Fprintln(os.Stderr, "usage: cairn [--version]")
	os.Exit(2)
}
