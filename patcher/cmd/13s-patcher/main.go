// Command 13s-patcher is the 13 Sentinels Unburger Patch Builder: it applies a
// patch-data bundle to a game archive and writes a new archive without modifying
// the original.
//
// Run it with no arguments to open the interactive dashboard on a terminal, or
// with a subcommand (status, doctor, preflight, build, validate, inspect,
// version) for scripted use.
package main

import (
	"os"

	"13s-patcher/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
