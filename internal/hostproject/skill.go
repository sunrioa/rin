package hostproject

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sunrioa/rin/host"
)

type SkillOptions struct {
	Root         string
	ID           string
	Version      string
	Description  string
	InputSchema  []byte
	OutputSchema []byte
	Effect       host.EffectClass
	Execution    host.ExecutionMode
	Risk         host.RiskLevel
}

func AddSkill(options SkillOptions) (string, error) {
	report, err := Inspect(options.Root)
	if err != nil {
		return "", err
	}
	if options.Version == "" {
		options.Version = "1.0.0"
	}
	if options.Description == "" {
		options.Description = "Game-authored capability " + options.ID + "."
	}
	if options.Effect == "" {
		options.Effect = host.EffectAdvisory
	}
	if options.Execution == "" {
		options.Execution = host.ExecutionImmediate
	}
	if options.Risk == "" {
		options.Risk = host.RiskLow
	}
	defaultSchema := []byte(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
			`"type":"object","properties":{},"additionalProperties":false}`)
	if len(options.InputSchema) == 0 {
		options.InputSchema = defaultSchema
	}
	if len(options.OutputSchema) == 0 {
		options.OutputSchema = defaultSchema
	}
	input, err := host.NewSchema(options.InputSchema)
	if err != nil {
		return "", fmt.Errorf("input schema: %w", err)
	}
	output, err := host.NewSchema(options.OutputSchema)
	if err != nil {
		return "", fmt.Errorf("output schema: %w", err)
	}
	descriptor, err := host.SealDescriptor(host.CapabilityDescriptor{
		Capability:  host.CapabilityRef{ID: options.ID, Version: options.Version},
		Description: options.Description,
		Input:       input, Output: output,
		Effect: options.Effect, Execution: options.Execution, Risk: options.Risk,
		RequiredDurability: host.DurabilityAdvisory,
		ExecutionBudget:    host.Duration{Clock: host.ClockEvent, Value: 1},
		MaxInputBytes:      1024, MaxOutputBytes: 1024,
		Cancellation: host.CancellationUnsupported,
		Reversible:   options.Effect == host.EffectAdvisory,
	})
	if err != nil {
		return "", err
	}
	name := descriptor.Capability.ID + "@" + descriptor.Capability.Version + ".json"
	if strings.ContainsAny(name, `<>:"/\|?*`) {
		return "", errors.New("capability identity is not portable as a file name")
	}
	directory := filepath.Join(report.Root, "capabilities")
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("capabilities path must be a real directory")
		}
	} else if errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(directory, 0o755); err != nil {
			return "", fmt.Errorf("create capabilities directory: %w", err)
		}
	} else {
		return "", err
	}
	// Keep embedded schema documents canonical. json.MarshalIndent rewrites
	// RawMessage whitespace and would invalidate the sealed descriptor digest.
	payload, err := json.Marshal(descriptor)
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	target := filepath.Join(directory, name)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create capability: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		_ = os.Remove(target)
		return "", fmt.Errorf("write capability: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("close capability: %w", err)
	}
	return target, nil
}
