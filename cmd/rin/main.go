package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

var version = "0.7.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rin:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return writeRootHelp(os.Stdout)
	}
	switch arguments[0] {
	case "help", "--help", "-h":
		return writeRootHelp(os.Stdout)
	case "version":
		if len(arguments) != 1 {
			return errors.New("version does not accept arguments")
		}
		_, err := fmt.Fprintln(os.Stdout, version)
		return err
	case "init":
		return runInit(arguments[1:], os.Stdout)
	case "add":
		return runAdd(arguments[1:], os.Stdout)
	case "conformance":
		return runConformance(arguments[1:], os.Stdout)
	case "doctor":
		return runDoctor(arguments[1:], os.Stdout)
	case "mcp":
		return runMCP(
			context.Background(),
			arguments[1:],
			os.Stdin,
			os.Stdout,
			os.Stderr,
		)
	default:
		return fmt.Errorf("unknown command %q; run rin --help", arguments[0])
	}
}

func writeRootHelp(output *os.File) error {
	_, err := fmt.Fprint(output, `Usage:
  rin init host [options]
  rin add skill [options]
  rin conformance host [options]
  rin doctor host [options]
  rin mcp [install|status|update|uninstall]
  rin version

Rin game Hosts connect to the separately managed rin-control daemon. The
retired Session/Proposal Sidecar is not part of Harness V2.
`)
	return err
}
