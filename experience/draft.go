package experience

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/provider"
)

var (
	portableIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	uuidPattern       = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	coordinatePattern = regexp.MustCompile(`(?i)(?:\(|\b)[xyz]?\s*=?\s*-?\d{1,8}\s*[, ]+\s*[xyz]?\s*=?\s*-?\d{1,8}\s*[, ]+\s*[xyz]?\s*=?\s*-?\d{1,8}(?:\)|\b)`)
	secretPattern     = regexp.MustCompile(`(?i)\b(?:sk|api[_-]?key|bearer)[-_:= ]?[a-z0-9]{12,}\b`)
)

var skillDraftSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["description","instructions"],
  "properties":{
    "description":{"type":"string","minLength":1,"maxLength":500},
    "instructions":{"type":"string","minLength":1,"maxLength":16000}
  }
}`)

type DraftRequest struct {
	Episode Episode `json:"episode"`
	SkillID string  `json:"skill_id"`
	Adapter string  `json:"adapter"`
}

type SkillDraft struct {
	SkillID      string         `json:"skill_id"`
	Version      string         `json:"version"`
	Description  string         `json:"description"`
	Instructions string         `json:"instructions"`
	Triggers     []string       `json:"triggers,omitempty"`
	Adapters     []string       `json:"adapters,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Usage        provider.Usage `json:"usage"`
	Model        string         `json:"model,omitempty"`
}

type DraftGenerator interface {
	Generate(context.Context, DraftRequest) (SkillDraft, error)
}

type ModelDraftGenerator struct {
	Provider  provider.StructuredGenerationProvider
	MaxTokens int
}

func (generator ModelDraftGenerator) Generate(
	ctx context.Context,
	request DraftRequest,
) (SkillDraft, error) {
	if ctx == nil || generator.Provider == nil {
		return SkillDraft{}, errors.New("draft generator requires context and provider")
	}
	request.SkillID = strings.TrimSpace(request.SkillID)
	request.Adapter = strings.TrimSpace(request.Adapter)
	if !portableIDPattern.MatchString(request.SkillID) || len(request.SkillID) > 96 {
		return SkillDraft{}, errors.New("skill id is invalid")
	}
	if request.Adapter != "" &&
		(!portableIDPattern.MatchString(request.Adapter) || len(request.Adapter) > 96) {
		return SkillDraft{}, errors.New("adapter id is invalid")
	}
	if request.Episode.ContractVersion != ContractVersion ||
		request.Episode.VerifiedResult == nil || !request.Episode.VerifiedResult.Success {
		return SkillDraft{}, errors.New("only verified successful experience can produce a skill draft")
	}
	portable := portableEpisode(request.Episode)
	payload, err := json.Marshal(portable)
	if err != nil {
		return SkillDraft{}, err
	}
	maxTokens := generator.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1_200
	}
	if maxTokens < 128 || maxTokens > 4_096 {
		return SkillDraft{}, errors.New("skill draft token budget is invalid")
	}
	response, err := generator.Provider.Complete(ctx, provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: "Turn verified game-task evidence into concise reusable procedural guidance. Describe preconditions, ordered actions, checks, and recovery. Never include coordinates, object IDs, private player data, credentials, hidden reasoning, or claims not supported by the evidence. Return only the requested JSON object."},
			{Role: "user", Content: string(payload)},
		},
		Schema: &provider.ResponseSchema{
			Name: "rin_skill_draft", Strict: true, Schema: skillDraftSchema,
		},
		Temperature: 0.1, MaxTokens: maxTokens,
	})
	if err != nil {
		return SkillDraft{}, err
	}
	var generated struct {
		Description  string `json:"description"`
		Instructions string `json:"instructions"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(response.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&generated); err != nil {
		return SkillDraft{}, fmt.Errorf("decode skill draft: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SkillDraft{}, errors.New("skill draft contains multiple JSON values")
	}
	description := redactPortableText(strings.TrimSpace(generated.Description))
	instructions := redactPortableText(strings.TrimSpace(generated.Instructions))
	if err := validateText("description", description, 500, true); err != nil {
		return SkillDraft{}, err
	}
	if err := validateText("instructions", instructions, 16_000, true); err != nil {
		return SkillDraft{}, err
	}
	capabilities := episodeCapabilities(request.Episode)
	adapters := []string(nil)
	if request.Adapter != "" {
		adapters = []string{request.Adapter}
	}
	return SkillDraft{
		SkillID: request.SkillID, Version: "v1", Description: description,
		Instructions: instructions, Triggers: append([]string(nil), request.Episode.Tags...),
		Adapters: adapters, Capabilities: capabilities,
		Usage: response.Usage, Model: response.Model,
	}, nil
}

type portableEpisodeView struct {
	Goal        string              `json:"goal,omitempty"`
	Tags        []string            `json:"tags,omitempty"`
	Events      []portableEventView `json:"events"`
	Corrections []string            `json:"corrections,omitempty"`
}

type portableEventView struct {
	Kind         string `json:"kind"`
	CapabilityID string `json:"capability_id,omitempty"`
	Summary      string `json:"summary,omitempty"`
	OutcomeCode  string `json:"outcome_code,omitempty"`
}

func portableEpisode(episode Episode) portableEpisodeView {
	view := portableEpisodeView{
		Goal: redactPortableText(episode.Goal), Tags: append([]string(nil), episode.Tags...),
		Events: make([]portableEventView, 0, len(episode.Events)),
	}
	for _, event := range episode.Events {
		item := portableEventView{
			Kind: event.Kind, Summary: redactPortableText(event.Summary),
			OutcomeCode: event.Evidence.OutcomeCode,
		}
		if event.Evidence.Capability != nil {
			item.CapabilityID = event.Evidence.Capability.ID
		}
		view.Events = append(view.Events, item)
	}
	for _, correction := range episode.Corrections {
		view.Corrections = append(view.Corrections, redactPortableText(correction.Summary))
	}
	return view
}

func episodeCapabilities(episode Episode) []string {
	set := make(map[string]struct{})
	for _, event := range episode.Events {
		if event.Evidence.Capability != nil {
			set[event.Evidence.Capability.ID] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for capability := range set {
		result = append(result, capability)
	}
	slices.Sort(result)
	return result
}

func redactPortableText(value string) string {
	value = uuidPattern.ReplaceAllString(value, "[world-reference]")
	value = coordinatePattern.ReplaceAllString(value, "[location]")
	value = secretPattern.ReplaceAllString(value, "[credential]")
	if !utf8.ValidString(value) {
		return ""
	}
	return value
}
