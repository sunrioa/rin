// Package provider defines a small, vendor-neutral completion contract.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ResponseSchema struct {
	Name   string
	Strict bool
	Schema json.RawMessage
}

type CompletionRequest struct {
	Messages    []Message
	Schema      *ResponseSchema
	Temperature float64
	MaxTokens   int
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type CompletionResponse struct {
	Content      string
	Model        string
	FinishReason string
	Usage        Usage
}

type Client interface {
	// Complete must stop its work and return promptly after ctx.Done is
	// closed. Resilient attempt and total timeouts rely on this cooperative
	// cancellation contract; Go cannot safely preempt an arbitrary blocking
	// implementation.
	Complete(ctx context.Context, request CompletionRequest) (CompletionResponse, error)
}

type Error struct {
	Kind       string
	Code       string
	StatusCode int
	Retryable  bool
	RetryAfter time.Duration
	// ProviderReached is true only after receiving a provider/protocol
	// response. Local validation, encoding, and request-construction errors
	// must leave it false so they remain neutral to the availability breaker.
	ProviderReached bool
	Cause           error
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("provider request failed: status=%d code=%s", e.StatusCode, safeCode(e.Code))
	}
	return "provider request failed: " + safeCode(e.Kind)
}

func (e *Error) Unwrap() error { return e.Cause }

func IsRetryable(err error) bool {
	var providerError *Error
	return errors.As(err, &providerError) && providerError.Retryable
}

func RetryDelay(err error) time.Duration {
	var providerError *Error
	if errors.As(err, &providerError) && providerError.RetryAfter > 0 {
		return providerError.RetryAfter
	}
	return 0
}

func safeCode(value string) string {
	if value == "" {
		return "unknown"
	}
	runes := []rune(value)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	for index, value := range runes {
		if !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_' || value == '-' || value == '.') {
			runes[index] = '_'
		}
	}
	return string(runes)
}
