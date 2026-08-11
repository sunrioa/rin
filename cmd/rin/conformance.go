package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/hostproject"
)

func runConformance(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("conformance requires a resource type (supported: host)")
	}
	if arguments[0] != "host" {
		return fmt.Errorf(
			"unsupported conformance resource %q (supported: host)", arguments[0])
	}
	flags := flag.NewFlagSet("rin conformance host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("path", ".", "Host project directory")
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := io.WriteString(output, `Usage:
  rin conformance host [-path HOST_PROJECT]

Validates the protocol version, deterministic scaffold manifest, generated file
digests, Windows-portable paths, and sealed capability descriptors.
`)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	report, err := hostproject.Inspect(*root)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		output,
		"Host %s conforms to %s: %d generated files, %d modified, %d capabilities.\n",
		report.Manifest.Project.ID, host.ActionContractVersion, report.CheckedFiles,
		len(report.ModifiedFiles), len(report.Capabilities),
	)
	return nil
}
