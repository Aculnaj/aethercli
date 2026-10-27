package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/Aculnaj/aethercli/internal/commands"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(commands.Execute(ctx, commands.Deps{}, os.Args[1:]))
}
