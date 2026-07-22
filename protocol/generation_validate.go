package protocol

import (
	"fmt"
	"math"
	"regexp"
)

var contextHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var generationKinds = map[string]struct{}{
	"director":           {},
	"story":              {},
	"scene":              {},
	"decision":           {},
	"ending":             {},
	"free-response":      {},
	"storylet-selection": {},
}

func ValidateGeneration(request GenerationRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("request_id", request.RequestID); err != nil {
		return err
	}
	if _, ok := generationKinds[request.Kind]; !ok {
		return &ValidationError{Field: "kind", Message: "must be a supported generation kind"}
	}
	if !contextHashPattern.MatchString(request.ContextHash) {
		return &ValidationError{Field: "context_hash", Message: "must be a lowercase SHA-256 digest"}
	}
	if len(request.Messages) == 0 || len(request.Messages) > 8 {
		return &ValidationError{Field: "messages", Message: "must contain 1-8 messages"}
	}
	totalCharacters := 0
	for index, message := range request.Messages {
		field := fmt.Sprintf("messages[%d]", index)
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return &ValidationError{Field: field + ".role", Message: "must be system, user, or assistant"}
		}
		if err := validateText(field+".content", message.Content, 32768, true); err != nil {
			return err
		}
		totalCharacters += len([]rune(message.Content))
	}
	if totalCharacters > 131072 {
		return &ValidationError{Field: "messages", Message: "must contain at most 131072 characters in total"}
	}
	if math.IsNaN(request.Temperature) || math.IsInf(request.Temperature, 0) || request.Temperature < 0 || request.Temperature > 2 {
		return &ValidationError{Field: "temperature", Message: "must be between 0 and 2"}
	}
	if request.MaxTokens < 1 || request.MaxTokens > 8192 {
		return &ValidationError{Field: "max_tokens", Message: "must be between 1 and 8192"}
	}
	if request.ResponseFormat != "json_object" {
		return &ValidationError{Field: "response_format", Message: "must equal json_object"}
	}
	return nil
}
