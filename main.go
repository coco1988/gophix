// gophix restores Google Photos Takeout metadata (capture dates, GPS,
// description) into media files and organizes them by year.
package main

import (
	"os"

	_ "time/tzdata" // embed timezone database so --timezone works everywhere

	"github.com/alexdachin/gophix/commands"
)

func main() {
	os.Exit(commands.Run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}
