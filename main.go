package main

import (
	"os"

	// Embed the IANA timezone database so --timezone Europe/Berlin works on
	// Windows and in any scratch container without OS tz packages.
	_ "time/tzdata"

	"github.com/alexdachin/gophix/commands"
)

func main() {
	os.Exit(commands.Run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}
