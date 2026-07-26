package hostscaffold

import (
	"encoding/json"
	"fmt"

	"github.com/sunrioa/rin/host"
)

func renderCustom(options normalizedOptions) ([]renderedFile, error) {
	hostConfig := map[string]any{
		"schema_version":   1,
		"protocol_version": "rin.protocol/v2",
		"project_id":       options.ID,
		"engine":           "custom",
		"runtime":          options.Runtime,
		"durability":       "advisory",
		"capability_paths": []string{"capabilities"},
	}
	config, err := json.MarshalIndent(hostConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode custom host config: %w", err)
	}
	config = append(config, '\n')
	input, err := host.NewSchema([]byte(`{
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {
      "line_id": {
        "type": "string",
        "minLength": 1,
        "maxLength": 96
      }
    },
    "required": ["line_id"],
    "additionalProperties": false
  }`))
	if err != nil {
		return nil, fmt.Errorf("build custom input schema: %w", err)
	}
	output, err := host.NewSchema([]byte(`{
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {
      "shown": {"type": "boolean"}
    },
    "required": ["shown"],
    "additionalProperties": false
  }`))
	if err != nil {
		return nil, fmt.Errorf("build custom output schema: %w", err)
	}
	descriptor, err := host.SealDescriptor(host.CapabilityDescriptor{
		Capability:         host.CapabilityRef{ID: "dialogue.say", Version: "1.0.0"},
		Description:        "Show one game-authored dialogue line.",
		Input:              input,
		Output:             output,
		Effect:             host.EffectAdvisory,
		Execution:          host.ExecutionImmediate,
		Risk:               host.RiskLow,
		RequiredDurability: host.DurabilityAdvisory,
		ExecutionBudget:    host.Duration{Clock: host.ClockEvent, Value: 1},
		MaxInputBytes:      1024,
		MaxOutputBytes:     1024,
		Cancellation:       host.CancellationUnsupported,
		Reversible:         true,
	})
	if err != nil {
		return nil, fmt.Errorf("seal custom capability: %w", err)
	}
	// Keep embedded schema documents canonical. json.MarshalIndent rewrites
	// RawMessage whitespace and would invalidate the sealed descriptor digest.
	capability, err := json.Marshal(descriptor)
	if err != nil {
		return nil, fmt.Errorf("encode custom capability: %w", err)
	}
	capability = append(capability, '\n')
	return []renderedFile{
		{
			Path: "rin-host.json", Mode: 0o644, Role: "host-contract",
			Data: config,
		},
		{
			Path: "capabilities/dialogue.say.json", Mode: 0o644,
			Role: "capability-descriptor", Data: capability,
		},
		{
			Path: "src/README.md", Mode: 0o644, Role: "integration-entry",
			Data: []byte(customEntryReadme(options)),
		},
	}, nil
}

func customEntryReadme(options normalizedOptions) string {
	return fmt.Sprintf(`# Host adapter entry

Runtime: %s

Implement these engine-owned boundaries:

1. Capture immutable observations on the authority thread.
2. Persist the Pending Turn before submitting it to Rin.
3. Resolve only registered capability IDs and validate their exact descriptor
   digest, epoch, deadline, targets, and arguments.
4. Execute on the authority thread with a stable operation ID.
5. Persist the exact Action Report in the same save boundary as the effect,
   then retry reporting without repeating the effect.

Do not place engine objects, threads, sockets, futures, tokens, model output, or
arbitrary command strings in persisted workflow state.
`, options.Runtime)
}
