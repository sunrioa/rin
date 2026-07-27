package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sunrioa/rin/internal/hostproject"
)

func runDoctor(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("doctor requires a resource type (supported: host)")
	}
	if arguments[0] != "host" {
		return fmt.Errorf("unsupported doctor resource %q (supported: host)", arguments[0])
	}
	flags := flag.NewFlagSet("rin doctor host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("path", ".", "Host project directory")
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := io.WriteString(output, `Usage:
  rin doctor host [-path HOST_PROJECT]

Runs Host conformance checks and a bounded runtime version probe. Runtime
absence or an unusable command shim is reported as a warning because the same
project may be built on another supported platform.
`)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	report, err := hostproject.Doctor(*root)
	if err != nil {
		return err
	}
	version := ""
	if report.Version != "" {
		version = fmt.Sprintf(" version=%q", report.Version)
	}
	detail := ""
	if report.Detail != "" {
		detail = fmt.Sprintf(" detail=%q", report.Detail)
	}
	fmt.Fprintf(
		output,
		"Host %s: conformance=pass platform=%s runtime=%s executable=%s status=%s%s%s.\n",
		report.Conformance.Manifest.Project.ID, report.Platform, report.Runtime,
		report.Executable, report.Status, version, detail,
	)
	return nil
}
