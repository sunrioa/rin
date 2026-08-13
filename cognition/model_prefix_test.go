package cognition

import (
	"encoding/json"
	"testing"
)

func TestStableModelPacketTracksDecisionSchema(t *testing.T) {
	input := ModelInput{}
	before, err := json.Marshal(buildModelV2StablePacket(input))
	if err != nil {
		t.Fatal(err)
	}
	original := append(json.RawMessage(nil), modelV2DecisionSchema...)
	defer func() { modelV2DecisionSchema = original }()
	modelV2DecisionSchema = json.RawMessage(`{"type":"object","properties":{"changed":{"type":"boolean"}}}`)
	after, err := json.Marshal(buildModelV2StablePacket(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("decision schema change did not invalidate the stable model packet")
	}
}
