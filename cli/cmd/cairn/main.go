package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, config.DefaultEnvironment()))
}

const usage = "usage: cairn [--help | --version]\n       cairn backup --job NAME [--config PATH]\n       cairn config check [--config PATH]\n"
const checkUsage = "usage: cairn config check [--config PATH]\n"

func run(args []string, stdout, stderr io.Writer, env config.Environment) int {
	if len(args) == 1 {
		switch args[0] {
		case "--version":
			fmt.Fprintf(stdout, "cairn %s\n", version)
			return 0
		case "--help", "-h":
			fmt.Fprint(stdout, usage)
			return 0
		}
	}
	if len(args) == 2 && args[0] == "config" && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprint(stdout, checkUsage)
		return 0
	}
	if len(args) > 0 && args[0] == "backup" {
		return runBackup(args[1:], stdout, stderr, env)
	}
	if len(args) < 2 || args[0] != "config" || args[1] != "check" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("config check", flag.ContinueOnError)
	// flag errors can echo arbitrary arguments; use controlled diagnostics.
	flags.SetOutput(io.Discard)
	var explicit string
	flags.StringVar(&explicit, "config", "", "configuration file")
	if err := flags.Parse(args[2:]); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(stdout, checkUsage)
			return 0
		}
		fmt.Fprint(stderr, checkUsage)
		return 2
	}
	emptyConfig := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "config" && explicit == "" {
			emptyConfig = true
		}
	})
	if flags.NArg() != 0 || emptyConfig {
		fmt.Fprint(stderr, checkUsage)
		return 2
	}
	if _, err := env.LoadValidated(explicit); err != nil {
		fmt.Fprintf(stderr, "cairn: %v\n", err)
		return 3
	}
	fmt.Fprintln(stdout, "Configuration is valid.")
	return 0
}
