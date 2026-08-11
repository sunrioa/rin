package controlplane

import "testing"

func TestDecisionAuthorityValidation(t *testing.T) {
	tests := []struct {
		name      string
		authority DecisionAuthority
	}{
		{
			name: "internal principal",
			authority: DecisionAuthority{
				Source:                DecisionInternal,
				ControllerPrincipalID: "player.one",
				Revision:              1,
				PersonaMode:           PersonaCharacterBound,
			},
		},
		{
			name: "internal avatar",
			authority: DecisionAuthority{
				Source:      DecisionInternal,
				Revision:    1,
				PersonaMode: PersonaAgentAvatar,
			},
		},
		{
			name: "external missing principal",
			authority: DecisionAuthority{
				Source:      DecisionExternal,
				Revision:    1,
				PersonaMode: PersonaCharacterBound,
			},
		},
		{
			name: "zero revision",
			authority: DecisionAuthority{
				Source:                DecisionExternal,
				ControllerPrincipalID: "player.one",
				PersonaMode:           PersonaCharacterBound,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publication := worldPublication(1, "ready")
			publication.Actors[0].Authority = &test.authority
			if err := validatePublication(
				publication,
				registration("instance.authority").Manifest,
				"test.host",
			); err == nil {
				t.Fatal("invalid authority was accepted")
			}
		})
	}
}
