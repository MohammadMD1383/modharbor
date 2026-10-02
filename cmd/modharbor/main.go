// Command modharbor keeps Minecraft mods working across game versions.
//
// It identifies installed mods by content hash, finds the newest build
// compatible with a given Minecraft version and loader, and installs it
// safely. The migration path is the primary workflow: point it at an old
// instance and a fresh one, and it does the tedious work.
package main

import (
	"os"

	"github.com/MohammadMD1383/modharbor/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
