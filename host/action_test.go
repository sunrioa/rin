package host

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sunrioa/rin/internal/jsonwire"
)

func TestV2RegistryBindsAndAuthorizesHostEffects(t *testing.T) {
	registry, err := NewRegistry(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := registry.RegisterSpec(testCapabilitySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Digest == "" || sealed.RequiredScopes[0] != "rin.actor.move" {
		t.Fatalf("capability spec was not normalized: %+v", sealed)
	}
	if snapshot := registry.SnapshotSpecs(); snapshot.Revision != 1 || len(snapshot.Specs) != 1 {
		t.Fatalf("unexpected capability snapshot: %+v", snapshot)
	}

	epoch := testEpoch()
	target := HostRef{
		Namespace: "test.world",
		Type:      "location",
		Key:       "dock",
		Epoch:     epoch,
	}
	request := ActionRequest{
		RequestID:      "request.move.1",
		ControllerID:   "controller.external.1",
		ActorID:        "actor.guide",
		Capability:     sealed.Capability,
		SpecDigest:     sealed.Digest,
		Arguments:      json.RawMessage(`{ "target": "dock", "count": 2 }`),
		Targets:        []HostRef{target},
		ExpectedEpoch:  epoch,
		ObservationSeq: 7,
		TaskID:         "task.reach.dock",
		IdempotencyKey: "request.move.1",
	}
	effects := []Effect{
		{
			EffectID:   "effect.move.position",
			Kind:       "world.position",
			Operation:  EffectOperationUpdate,
			Subject:    &target,
			Tags:       []string{"world.travel", "actor.movement"},
			Ownership:  OwnershipActor,
			Scope:      "world.public",
			Quantity:   12,
			Unit:       "block",
			Reversible: true,
			Risk:       RiskModerate,
			Attributes: json.RawMessage(`{ "mode": "walk", "distance": 12 }`),
		},
		{
			EffectID:   "effect.move.stamina",
			Kind:       "actor.stamina",
			Operation:  EffectOperationConsume,
			Subject:    &target,
			Tags:       []string{"world.travel", "actor.resource"},
			Ownership:  OwnershipActor,
			Scope:      "actor.self",
			Quantity:   1,
			Unit:       "point",
			Reversible: false,
			Risk:       RiskModerate,
			Attributes: json.RawMessage(`{"mode":"walk","distance":12}`),
		},
	}
	action, err := registry.SealBinding(
		request,
		BindingDraft{
			BindingID:       "binding.move.1",
			ResolvedTargets: []HostRef{target},
			Effects:         effects,
			ValidUntil:      Timepoint{Clock: ClockRealtime, Value: 15_000},
		},
		Timepoint{Clock: ClockRealtime, Value: 10_000},
		epoch,
		7,
	)
	if err != nil {
		t.Fatal(err)
	}
	const expectedSpecDigest = "eb6781411eb3558d55c01dd710952c06111f0d1c3d0a220eef0824f6b5806f38"
	const expectedRequestDigest = "8227d8c696b50aeb5c74323d3bb3c978576c617c516a72628db730d4ee1d69d7"
	const expectedEffectDigest = "de4b9b1f3329993edcf4e9cdd045f1a6a558db48a025b2a0fa9597431f530619"
	if sealed.Digest != expectedSpecDigest || action.RequestDigest != expectedRequestDigest ||
		action.EffectDigest != expectedEffectDigest {
		t.Fatalf(
			"V2 digest drift: spec=%s request=%s effects=%s",
			sealed.Digest,
			action.RequestDigest,
			action.EffectDigest,
		)
	}
	if err := ValidateBoundAction(action); err != nil {
		t.Fatalf("bound action failed structural validation: %v", err)
	}
	if string(action.NormalizedArguments) != `{"count":2,"target":"dock"}` {
		t.Fatalf("arguments were not canonicalized: %s", action.NormalizedArguments)
	}
	if action.Effects[0].EffectID != "effect.move.position" ||
		action.Effects[0].Tags[0] != "actor.movement" {
		t.Fatalf("effects were not normalized: %+v", action.Effects)
	}

	request.Arguments[0] = 'x'
	effects[0].Attributes[0] = 'x'
	if action.NormalizedArguments[0] == 'x' || action.Effects[0].Attributes[0] == 'x' {
		t.Fatal("caller mutated a bound action through aliased input")
	}
	principal := Principal{ID: "principal.guide", GrantedScopes: []string{"rin.actor.move"}}
	if err := registry.AuthorizeBoundAction(
		action,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		7,
		principal,
	); err != nil {
		t.Fatalf("valid bound action rejected: %v", err)
	}
	if err := registry.ValidateSpecOutput(
		sealed.Capability,
		sealed.Digest,
		[]byte(`{"state":"reached"}`),
	); err != nil {
		t.Fatalf("valid capability output rejected: %v", err)
	}

	forged := cloneBoundAction(action)
	forged.Effects[0].Ownership = OwnershipUnowned
	forged.EffectDigest, err = EffectPreviewDigest(forged.Effects)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBoundAction(forged); err != nil {
		t.Fatalf("forged action should remain structurally valid for this test: %v", err)
	}
	if err := registry.AuthorizeBoundAction(
		forged,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		7,
		principal,
	); err == nil || !strings.Contains(err.Error(), "Host binding data was modified") {
		t.Fatalf("forged Host effect was not rejected: %v", err)
	}

	if err := registry.AuthorizeBoundAction(
		action,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		8,
		principal,
	); err != nil {
		t.Fatal("recent observation within the bounded window was rejected")
	}
	if err := registry.AuthorizeBoundAction(
		action,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		16,
		principal,
	); err == nil {
		t.Fatal("stale observation sequence was authorized")
	}
	if err := registry.AuthorizeBoundAction(
		action,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		7,
		Principal{ID: "principal.viewer"},
	); err == nil {
		t.Fatal("principal without required scope was authorized")
	}
	if !registry.UnregisterSpec(sealed.Capability) {
		t.Fatal("registered V2 capability was not removed")
	}
	if err := registry.AuthorizeBoundAction(
		action,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		7,
		principal,
	); err == nil {
		t.Fatal("binding remained authorized after capability revocation")
	}
}

func TestV2RegistryRejectsInvalidRequestsAndEffects(t *testing.T) {
	registry, err := NewRegistry(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := registry.RegisterSpec(testCapabilitySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	epoch := testEpoch()
	request := testActionRequest(sealed, epoch)

	duplicateArgument := request
	duplicateArgument.Arguments = json.RawMessage(`{"target":"dock","target":"gate","count":2}`)
	if err := registry.ValidateRequest(duplicateArgument, epoch, 7); err == nil {
		t.Fatal("ambiguous duplicate JSON fields were accepted")
	}
	stale := epoch
	stale.World++
	if err := registry.ValidateRequest(request, stale, 7); err == nil {
		t.Fatal("request from a stale epoch was accepted")
	}
	if err := registry.ValidateRequest(request, epoch, 8); err != nil {
		t.Fatal("request within the recent observation window was rejected")
	}
	if err := registry.ValidateRequest(request, epoch, 16); err == nil {
		t.Fatal("request from a stale observation was accepted")
	}

	target := request.Targets[0]
	lowRisk := []Effect{{
		EffectID:   "effect.move",
		Kind:       "world.position",
		Operation:  EffectOperationUpdate,
		Subject:    &target,
		Tags:       []string{"actor.movement"},
		Ownership:  OwnershipActor,
		Scope:      "world.public",
		Risk:       RiskLow,
		Attributes: json.RawMessage(`{"distance":1,"mode":"walk"}`),
	}}
	if _, err := registry.SealBinding(
		request,
		BindingDraft{
			BindingID:       "binding.low-risk",
			ResolvedTargets: request.Targets,
			Effects:         lowRisk,
			ValidUntil:      Timepoint{Clock: ClockRealtime, Value: 20},
		},
		Timepoint{Clock: ClockRealtime, Value: 10},
		epoch,
		7,
	); err == nil {
		t.Fatal("effect below the capability risk floor was accepted")
	}
	invalidAttributes := cloneEffects(lowRisk)
	invalidAttributes[0].Risk = RiskModerate
	invalidAttributes[0].Attributes = json.RawMessage(`{"distance":1,"mode":"walk","secret":"x"}`)
	if _, err := registry.SealBinding(
		request,
		BindingDraft{
			BindingID:       "binding.invalid-attributes",
			ResolvedTargets: request.Targets,
			Effects:         invalidAttributes,
			ValidUntil:      Timepoint{Clock: ClockRealtime, Value: 20},
		},
		Timepoint{Clock: ClockRealtime, Value: 10},
		epoch,
		7,
	); err == nil {
		t.Fatal("effect attributes outside the Host schema were accepted")
	}
	nestedAttributes := cloneEffects(lowRisk)
	nestedAttributes[0].Risk = RiskModerate
	nestedAttributes[0].Attributes = json.RawMessage(`{"distance":1,"mode":{"unsafe":true}}`)
	if _, err := registry.SealBinding(
		request,
		BindingDraft{
			BindingID:       "binding.nested-attributes",
			ResolvedTargets: request.Targets,
			Effects:         nestedAttributes,
			ValidUntil:      Timepoint{Clock: ClockRealtime, Value: 20},
		},
		Timepoint{Clock: ClockRealtime, Value: 10},
		epoch,
		7,
	); err == nil {
		t.Fatal("nested effect attributes were accepted")
	}
}

func TestObservationEnvelopeIsBoundedAndSchemaChecked(t *testing.T) {
	schema, err := NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"activity":{"type":"string"}},
		"required":["activity"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	epoch := testEpoch()
	actorRef := HostRef{Namespace: "test.world", Type: "actor", Key: "guide", Epoch: epoch}
	observation := ObservationEnvelope{
		ObservationID: "observation.7",
		HostID:        "host.test",
		WorldID:       epoch.WorldID,
		ActorID:       "actor.guide",
		Epoch:         epoch,
		Sequence:      7,
		ObservedAt:    Timepoint{Clock: ClockStep, Value: 7},
		Schema: SchemaRef{
			ID:      "rin.observation.actor",
			Version: "2.0.0",
			SHA256:  schema.SHA256,
		},
		Payload: json.RawMessage(`{"activity":"walking"}`),
		Facts: []ObservationFact{{
			FactID:  "fact.activity",
			Kind:    "actor.activity",
			Subject: &actorRef,
			Tags:    []string{"actor.visible"},
			Value:   json.RawMessage(`"walking"`),
		}},
		Resources: []ObservationResource{{
			Ref:        actorRef,
			Kind:       "actor.status",
			Tags:       []string{"actor.visible"},
			Ownership:  OwnershipActor,
			Scope:      "actor.self",
			Quantity:   1,
			Unit:       "actor",
			Attributes: json.RawMessage(`{"healthy":true}`),
		}},
		Artifacts: []ObservationArtifact{{
			ArtifactID: "artifact.view.7",
			Kind:       "world.image",
			MediaType:  "image/png",
			SizeBytes:  128,
			SHA256:     strings.Repeat("a", 64),
		}},
		ContinuationToken: "page.2",
	}
	if err := ValidateObservationPayload(observation, schema); err != nil {
		t.Fatalf("valid observation rejected: %v", err)
	}
	query := ObservationQuery{
		QueryID:       "query.actor.7",
		HostID:        observation.HostID,
		WorldID:       observation.WorldID,
		ActorID:       observation.ActorID,
		ExpectedEpoch: epoch,
		AfterSequence: 6,
		Kinds:         []string{"actor.status", "world.visible"},
		Limit:         64,
	}
	if err := ValidateObservationQuery(query); err != nil {
		t.Fatalf("valid observation query rejected: %v", err)
	}
	query.Kinds = []string{"world.visible", "actor.status"}
	if err := ValidateObservationQuery(query); err == nil {
		t.Fatal("unsorted observation kinds were accepted")
	}
	wrongPayload := observation
	wrongPayload.Payload = json.RawMessage(`{"activity":7}`)
	if err := ValidateObservationPayload(wrongPayload, schema); err == nil {
		t.Fatal("schema-invalid observation payload was accepted")
	}
	duplicateFact := observation
	duplicateFact.Facts = append(duplicateFact.Facts, duplicateFact.Facts[0])
	if err := ValidateObservationEnvelope(duplicateFact); err == nil {
		t.Fatal("duplicate observation fact was accepted")
	}
	staleResource := observation
	staleResource.Resources = append([]ObservationResource(nil), observation.Resources...)
	staleResource.Resources[0].Ref.Epoch.World++
	if err := ValidateObservationEnvelope(staleResource); err == nil {
		t.Fatal("stale observation resource was accepted")
	}
}

func TestCapabilitySpecAndRegistryUseDefensiveCopies(t *testing.T) {
	registry, err := NewRegistry(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := registry.RegisterSpec(testCapabilitySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed.RequiredScopes[0] = "rin.elevated"
	sealed.Input.Document[0] = 'x'
	resolved, ok := registry.ResolveSpec(sealed.Capability)
	if !ok {
		t.Fatal("registered capability disappeared")
	}
	if resolved.RequiredScopes[0] != "rin.actor.move" || resolved.Input.Document[0] == 'x' {
		t.Fatalf("caller mutated V2 registry state: %+v", resolved)
	}
}

func TestV2CanonicalJSONFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/v2_action_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := jsonwire.Validate(raw); err != nil {
		t.Fatalf("fixture is ambiguous JSON: %v", err)
	}
	var fixture struct {
		ContractVersion string        `json:"contract_version"`
		SpecDigest      string        `json:"capability_spec_digest"`
		Request         ActionRequest `json:"action_request"`
		RequestDigest   string        `json:"action_request_digest"`
		Effects         []Effect      `json:"effect_preview"`
		EffectDigest    string        `json:"effect_digest"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ContractVersion != ContractVersion {
		t.Fatalf("fixture contract version = %q", fixture.ContractVersion)
	}
	sealed, err := SealCapabilitySpec(testCapabilitySpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Digest != fixture.SpecDigest || fixture.Request.SpecDigest != fixture.SpecDigest {
		t.Fatalf("fixture spec digest drift: sealed=%s fixture=%s", sealed.Digest, fixture.SpecDigest)
	}
	requestDigest, err := ActionRequestDigest(fixture.Request)
	if err != nil {
		t.Fatal(err)
	}
	if requestDigest != fixture.RequestDigest {
		t.Fatalf("fixture request digest = %s, computed %s", fixture.RequestDigest, requestDigest)
	}
	for index, effect := range fixture.Effects {
		if err := ValidateEffect(effect, fixture.Request.ExpectedEpoch); err != nil {
			t.Fatalf("fixture effect %d: %v", index, err)
		}
	}
	effectDigest, err := EffectPreviewDigest(fixture.Effects)
	if err != nil {
		t.Fatal(err)
	}
	if effectDigest != fixture.EffectDigest {
		t.Fatalf("fixture effect digest = %s, computed %s", fixture.EffectDigest, effectDigest)
	}

	schemaDocument, err := os.ReadFile("../api/action-request-v2.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := NewSchema(schemaDocument)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON, err := json.Marshal(fixture.Request)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.ValidateInstance(requestJSON); err != nil {
		t.Fatalf("fixture does not satisfy public ActionRequest schema: %v", err)
	}
}

func FuzzValidateActionRequestDoesNotPanic(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"target":"dock","count":2}`),
		[]byte(`{"target":"first","target":"last","count":2}`),
		[]byte(`[]`),
		{0xff},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, arguments []byte) {
		request := ActionRequest{
			RequestID:      "request.fuzz.1",
			ControllerID:   "controller.fuzz.1",
			ActorID:        "actor.fuzz.1",
			Capability:     CapabilityRef{ID: "rin.test.action", Version: "2.0.0"},
			SpecDigest:     strings.Repeat("a", 64),
			Arguments:      append(json.RawMessage(nil), arguments...),
			ExpectedEpoch:  testEpoch(),
			ObservationSeq: 1,
			IdempotencyKey: "request.fuzz.1",
		}
		_ = ValidateActionRequest(request)
		_, _ = ActionRequestDigest(request)
	})
}

func testCapabilitySpec(t *testing.T) CapabilitySpec {
	t.Helper()
	input, err := NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"target":{"type":"string","minLength":1},
			"count":{"type":"integer","minimum":1,"maximum":8}
		},
		"required":["target","count"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"state":{"enum":["reached","unreachable","interrupted"]}},
		"required":["state"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	effectAttributes, err := NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"distance":{"type":"integer","minimum":0},
			"mode":{"enum":["walk","run"]}
		},
		"required":["distance","mode"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return CapabilitySpec{
		Capability:         CapabilityRef{ID: "rin.navigation.move-to", Version: "2.0.0"},
		Description:        "Move through the authoritative Host navigation system.",
		Input:              input,
		Output:             output,
		EffectSchema:       effectAttributes,
		Kind:               CapabilityAtomic,
		Execution:          ExecutionLongRunning,
		Cancellation:       CancellationCooperative,
		RiskFloor:          RiskModerate,
		RequiredDurability: DurabilityAdvisory,
		RequiredScopes:     []string{"rin.actor.move"},
		ExecutionBudget:    Duration{Clock: ClockRealtime, Value: 10_000},
		MaxInputBytes:      1024,
		MaxOutputBytes:     1024,
		MaxEffects:         4,
	}
}

func testActionRequest(spec CapabilitySpec, epoch Epoch) ActionRequest {
	return ActionRequest{
		RequestID:    "request.move.1",
		ControllerID: "controller.external.1",
		ActorID:      "actor.guide",
		Capability:   spec.Capability,
		SpecDigest:   spec.Digest,
		Arguments:    json.RawMessage(`{"target":"dock","count":2}`),
		Targets: []HostRef{{
			Namespace: "test.world",
			Type:      "location",
			Key:       "dock",
			Epoch:     epoch,
		}},
		ExpectedEpoch:  epoch,
		ObservationSeq: 7,
		TaskID:         "task.reach.dock",
		IdempotencyKey: "request.move.1",
	}
}
