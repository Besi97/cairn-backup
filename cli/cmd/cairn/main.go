package main

import (
	"os"

	"github.com/Besi97/cairn-backup/cli/internal/cli"
)

// version is overridden with -ldflags for versioned builds.
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
