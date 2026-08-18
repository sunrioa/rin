package story

import (
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

// NewPolicy returns the reference story policy. The character boundary is
// enforced from Host-authored effects and is shared by every controller.
func NewPolicy() (*policy.Engine, error) {
	return policy.New(policy.Config{
		Revision:         1,
		Profile:          policy.ProfileOpen,
		KnownEffectKinds: []string{"relation.update", "social.dialogue", "story.progress"},
		KnownScopes:      []string{scopeCharacterBoundary, scopePublic},
		Rules: []policy.Rule{{
			RuleID:       "story.character-boundary",
			Layer:        policy.LayerActor,
			Priority:     100,
			Result:       policy.Deny,
			Scopes:       []string{scopeCharacterBoundary},
			ReasonCode:   "story.character_boundary",
			HumanSummary: "Mira has not consented to discuss the sealed letter.",
		}},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 10},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
}

func schema(document string) (host.Schema, error) {
	return host.NewSchema([]byte(document))
}

func capabilitySpecs() ([]host.CapabilitySpec, error) {
	speakInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"text":{"type":"string","minLength":1,"maxLength":300}},
		"required":["text"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	speakOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"line_id":{"type":"string"},"text":{"type":"string"},"relation":{"type":"integer"}},
		"required":["line_id","text","relation"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	speakEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"speaker":{"type":"string"},"text":{"type":"string"},"delta":{"type":"integer"}},
		"required":["speaker","text","delta"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	topicInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"topic":{"type":"string","enum":["festival","photographs","sealed-letter"]}},
		"required":["topic"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	topicOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"scene":{"type":"string"},"topic":{"type":"string"}},
		"required":["scene","topic"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	topicEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"from_topic":{"type":"string"},"to_topic":{"type":"string"}},
		"required":["from_topic","to_topic"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	taskInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"task":{"type":"string","enum":["prepare-exhibit","restore-photograph"]}},
		"required":["task"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	taskOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"task":{"type":"string"},"accepted":{"type":"boolean"}},
		"required":["task","accepted"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	taskEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"task":{"type":"string"}},
		"required":["task"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	waitInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	waitOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"completed":{"type":"boolean"}},
		"required":["completed"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	waitEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"duration_steps":{"type":"integer","minimum":1}},
		"required":["duration_steps"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}

	base := func(
		id, description string,
		input, output, effects host.Schema,
	) host.CapabilitySpec {
		return host.CapabilitySpec{
			Capability:         host.CapabilityRef{ID: id, Version: CapabilityVersion},
			Description:        description,
			Input:              input,
			Output:             output,
			EffectSchema:       effects,
			Kind:               host.CapabilityAtomic,
			Execution:          host.ExecutionImmediate,
			Cancellation:       host.CancellationUnsupported,
			RiskFloor:          host.RiskLow,
			RequiredDurability: host.DurabilityAdvisory,
			RequiredScopes:     []string{controlplane.ScopeActorExecute},
			ExecutionBudget:    host.Duration{Clock: host.ClockStep, Value: 20},
			MaxInputBytes:      2_048,
			MaxOutputBytes:     2_048,
			MaxEffects:         4,
		}
	}
	specs := []host.CapabilitySpec{
		base(CapabilitySpeak, "Speak one character-authored line in the current scene. Return no target handles; the Host binds the current character.", speakInput, speakOutput, speakEffects),
		base(CapabilityWait, "Start a cancellable quiet moment. Return no target handles; the Host binds the current scene.", waitInput, waitOutput, waitEffects),
		base(CapabilityChangeTopic, "Change the current conversation topic. Return no target handles; the Host binds the current scene.", topicInput, topicOutput, topicEffects),
		base(CapabilityAcceptTask, "Accept one available story task. Return no target handles; the Host binds the current character.", taskInput, taskOutput, taskEffects),
	}
	specs[1].Execution = host.ExecutionLongRunning
	specs[1].Cancellation = host.CancellationCooperative
	return specs, nil
}
