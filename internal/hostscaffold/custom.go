package hostscaffold

import (
	"encoding/json"
	"fmt"

	"github.com/sunrioa/rin/host"
)

func renderCustom(options normalizedOptions) ([]renderedFile, error) {
	hostConfig := map[string]any{
		"schema_version":   2,
		"contract_version": host.ContractVersion,
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
	effectSchema, err := host.NewSchema([]byte(`{
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {},
    "additionalProperties": false
  }`))
	if err != nil {
		return nil, fmt.Errorf("build custom effect schema: %w", err)
	}
	spec, err := host.SealCapabilitySpec(host.CapabilitySpec{
		Capability:         host.CapabilityRef{ID: "dialogue.say", Version: "1.0.0"},
		Description:        "Show one game-authored dialogue line.",
		Input:              input,
		Output:             output,
		EffectSchema:       effectSchema,
		Kind:               host.CapabilityAtomic,
		Execution:          host.ExecutionImmediate,
		Cancellation:       host.CancellationUnsupported,
		RiskFloor:          host.RiskLow,
		RequiredDurability: host.DurabilityAdvisory,
		ExecutionBudget:    host.Duration{Clock: host.ClockEvent, Value: 1},
		MaxInputBytes:      1024,
		MaxOutputBytes:     1024,
		MaxEffects:         1,
	})
	if err != nil {
		return nil, fmt.Errorf("seal custom capability: %w", err)
	}
	// Keep embedded schema documents canonical. json.MarshalIndent rewrites
	// RawMessage whitespace and would invalidate the sealed descriptor digest.
	capability, err := json.Marshal(spec)
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

1. Publish stable Host, world, actor, epoch, observation, and capability data.
2. Bind each Action Request to the registered capability digest, arguments,
   targets, observation sequence, and current epoch.
3. Apply policy and control-authority checks immediately before execution.
4. Execute only registered effects on the authority thread with the supplied
   operation and idempotency identities.
5. Report authoritative progress and a terminal Action Outcome.

Do not place engine objects, threads, sockets, futures, tokens, arbitrary model
text, or unregistered command strings in durable action state.
`, options.Runtime)
}
