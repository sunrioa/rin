package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"

	"github.com/sunrioa/rin/internal/app"
)

func runServe(arguments []string) error {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		shutdownSignals()...,
	)
	defer stop()
	err := app.Run(ctx, arguments, os.LookupEnv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}
