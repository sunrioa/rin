package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/hostproject"
)

func runAdd(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("add requires a resource type (supported: skill)")
	}
	if arguments[0] != "skill" {
		return fmt.Errorf("unsupported add resource %q (supported: skill)", arguments[0])
	}
	flags := flag.NewFlagSet("rin add skill", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("path", ".", "Host project directory")
	id := flags.String("id", "", "namespaced capability identifier")
	version := flags.String("version", "1.0.0", "exact capability version")
	description := flags.String("description", "", "game-authored capability description")
	inputSchema := flags.String("input-schema", "", "JSON Schema file for input")
	outputSchema := flags.String("output-schema", "", "JSON Schema file for output")
	effectSchema := flags.String("effect-schema", "", "JSON Schema file for effect attributes")
	kind := flags.String("kind", string(host.CapabilityAtomic), "atomic or macro")
	execution := flags.String("execution", string(host.ExecutionImmediate), "immediate, queued, or long-running")
	cancellation := flags.String("cancellation", string(host.CancellationUnsupported), "unsupported, cooperative, or preemptive")
	riskFloor := flags.String("risk-floor", string(host.RiskLow), "low, moderate, high, or critical")
	maxEffects := flags.Uint("max-effects", 1, "maximum effects per bound action")
	producesChildren := flags.Bool("produces-child-operations", false, "macro creates auditable child operations")
	if err := flags.Parse(arguments[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return writeAddSkillHelp(output)
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *id == "" {
		return errors.New("-id is required")
	}
	input, err := readSkillSchema(*inputSchema)
	if err != nil {
		return fmt.Errorf("read input schema: %w", err)
	}
	outputSchemaDocument, err := readSkillSchema(*outputSchema)
	if err != nil {
		return fmt.Errorf("read output schema: %w", err)
	}
	effectSchemaDocument, err := readSkillSchema(*effectSchema)
	if err != nil {
		return fmt.Errorf("read effect schema: %w", err)
	}
	if *maxEffects == 0 || *maxEffects > 64 {
		return errors.New("-max-effects must be between 1 and 64")
	}
	target, err := hostproject.AddSkill(hostproject.SkillOptions{
		Root: *root, ID: *id, Version: *version, Description: *description,
		InputSchema: input, OutputSchema: outputSchemaDocument,
		EffectSchema: effectSchemaDocument,
		Kind:         host.CapabilityKind(*kind), Execution: host.ExecutionMode(*execution),
		Cancellation: host.CancellationMode(*cancellation),
		RiskFloor:    host.RiskLevel(*riskFloor), MaxEffects: uint32(*maxEffects),
		ProducesChildOperations: *producesChildren,
	})
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(*root, target)
	if err != nil {
		relative = target
	}
	fmt.Fprintf(output, "Created capability %s.\n", filepath.ToSlash(relative))
	fmt.Fprintln(output, "Run `rin conformance host` after implementing the capability.")
	return nil
}

func writeAddSkillHelp(output io.Writer) error {
	_, err := io.WriteString(output, `Usage:
  rin add skill -id CAPABILITY_ID [options]

Options:
  -path string          Host project directory (default ".")
  -id string            namespaced capability ID, for example dialogue.say
  -version string       exact semantic version (default "1.0.0")
  -description string   game-authored capability description
  -input-schema string  JSON Schema file for input; defaults to an empty object
  -output-schema string JSON Schema file for output; defaults to an empty object
  -effect-schema string JSON Schema file for effect attributes; defaults to an empty object
  -kind string          atomic or macro (default "atomic")
  -execution string     immediate, queued, or long-running (default "immediate")
  -cancellation string  unsupported, cooperative, or preemptive (default "unsupported")
  -risk-floor string    low, moderate, high, or critical (default "low")
  -max-effects uint     maximum effects per action, from 1 to 64 (default 1)
  -produces-child-operations
                        macro creates auditable child operations
`)
	return err
}

func readSkillSchema(path string) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("schema path must be a regular file")
	}
	if info.Size() > 64<<10 {
		return nil, errors.New("schema file exceeds 64 KiB")
	}
	return os.ReadFile(path)
}
