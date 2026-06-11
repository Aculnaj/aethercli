package main

import (
	"fmt"
	"os"

	"github.com/Aculnaj/aethercli/internal/commands"
)

func main() {
	if err := commands.NewRootCommand(commands.Deps{}).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
