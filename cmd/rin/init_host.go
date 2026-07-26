package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/sunrioa/rin/internal/hostscaffold"
)

func runInit(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("init requires a resource type (supported: host)")
	}
	if isHelpArgument(arguments[0]) {
		_, err := io.WriteString(output, `Usage:
  rin init host [options]

Resources:
  host    create a self-contained game Host scaffold
`)
		return err
	}
	if arguments[0] != "host" {
		return fmt.Errorf("unsupported init resource %q (supported: host)", arguments[0])
	}
	return runInitHost(arguments[1:], output)
}

func runInitHost(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("rin init host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	engine := flags.String("engine", "", "target engine")
	runtime := flags.String("runtime", "", "custom host runtime")
	id := flags.String("id", "", "portable Host machine identifier")
	name := flags.String("name", "", "human-readable Host name; defaults to -id")
	namespace := flags.String("namespace", "", "lowercase reverse-domain owner namespace")
	author := flags.String("author", "", "optional author; Luanti requires a ContentDB username")
	projectVersion := flags.String("version", "0.1.0", "new Host version")
	destination := flags.String("output", "", "relative output directory; defaults to -id")
	dryRun := flags.Bool("dry-run", false, "validate and list files without writing")
	listHosts := flags.Bool("list-hosts", false, "list supported host identifiers")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeInitHostHelp(output)
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *listHosts {
		var conflicting []string
		flags.Visit(func(set *flag.Flag) {
			if set.Name != "list-hosts" {
				conflicting = append(conflicting, "-"+set.Name)
			}
		})
		if len(conflicting) != 0 {
			return fmt.Errorf("-list-hosts cannot be combined with %s", strings.Join(conflicting, ", "))
		}
		return writeHostList(output)
	}
	if *engine == "" {
		return errors.New("-engine is required")
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	options := hostscaffold.Options{
		Host: *engine, Runtime: *runtime, ID: *id, Name: *name, Namespace: *namespace,
		Author: *author, Version: *projectVersion, Output: *destination,
	}
	if *dryRun {
		plan, err := hostscaffold.Render(options)
		if err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("read current directory: %w", err)
		}
		if err := plan.ValidateTargetAt(cwd); err != nil {
			return err
		}
		fmt.Fprintf(
			output, "Would create %s scaffold %q in %s (%d files):\n",
			plan.Host().ID, plan.Name(), plan.Output(), len(plan.Files()))
		for _, file := range plan.Files() {
			fmt.Fprintf(output, "  %s  %s\n", file.SHA256[:12], file.Path)
		}
		return nil
	}
	result, err := hostscaffold.Generate(options)
	if err != nil {
		return err
	}
	outputPath := options.Output
	if outputPath == "" {
		outputPath = options.ID
	}
	fmt.Fprintf(
		output,
		"Created %s scaffold %q in %s (%d files).\n",
		result.Host.ID, result.Name, outputPath, result.FileCount,
	)
	fmt.Fprintf(
		output,
		"Review %s before building or installing it in a real game.\n",
		path.Join(outputPath, "README.md"),
	)
	return nil
}

func writeInitHostHelp(output io.Writer) error {
	_, err := io.WriteString(output, `Usage:
  rin init host -engine ENGINE -id HOST_ID [options]
  rin init host -list-hosts

Options:
  -engine string      custom, fabric, bepinex-mono, bepinex-il2cpp, or luanti
  -runtime string     custom runtime: go, javascript, python, csharp, java, or lua
  -id string          portable 2-64 character Host machine identifier
  -name string        player-facing display name (defaults to -id)
  -namespace string   lowercase reverse-domain owner; required except for Luanti
  -author string      optional author; Luanti requires a ContentDB username
  -version string     new Host version (default "0.1.0")
  -output string      new relative directory below the current directory
  -dry-run            validate and list deterministic files without writing
  -list-hosts         list embedded host templates

The destination must not exist. The generator creates a self-contained project,
never downloads templates, and never overwrites an existing file. See
docs/host-scaffolding.md for build and real-game validation requirements.
`)
	return err
}

func isHelpArgument(argument string) bool {
	return argument == "-h" || argument == "--help" || argument == "-help"
}

func writeHostList(output io.Writer) error {
	for _, host := range hostscaffold.Hosts() {
		namespace := "unused"
		if host.RequiresNamespace {
			namespace = "required"
		}
		if _, err := fmt.Fprintf(
			output, "%-16s  namespace=%-8s  %-26s  %s\n",
			host.ID, namespace, host.TemplateStatus, host.Name,
		); err != nil {
			return err
		}
	}
	return nil
}
