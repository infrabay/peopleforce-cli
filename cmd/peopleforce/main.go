// peopleforce is a CLI for the PeopleForce HR API, built to be driven by
// humans and AI agents alike. See README.md for the output contract.
package main

import (
	"os"

	"github.com/3bagels/peopleforce-cli/internal/command"
)

func main() {
	root, app := command.NewRoot()
	if err := root.Execute(); err != nil {
		command.PrintError(os.Stderr, err, app.JSONErrors())
		os.Exit(command.CodeFor(err))
	}
}
