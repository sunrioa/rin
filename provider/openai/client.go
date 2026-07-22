// Package openai implements the OpenAI-compatible chat completions protocol.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/provider"
)

const defaultMaxResponseBytes int64 = 2 << 20

type Config struct {
	BaseURL          string
	APIKey           string
	Model            string
	ResponseFormat   string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type Client struct {
	endpoint         string
	apiKey           string
	model            string
	responseFormat   string
	httpClient       *http.Client
	maxResponseBytes int64
}

func New(config Config) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("provider base URL must be an http(s) URL without user information")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("provider model is required")
	}
	format := config.ResponseFormat
	if format == "" {
		format = "json_schema"
	}
	if format != "json_schema" && format != "json_object" && format != "none" {
		return nil, errors.New("response format must be json_schema, json_object, or none")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &clientCopy
	maximum := config.MaxResponseBytes
	if maximum <= 0 {
		maximum = defaultMaxResponseBytes
	}
	base := strings.TrimRight(parsed.String(), "/")
	return &Client{
		endpoint:         base + "/chat/completions",
		apiKey:           config.APIKey,
		model:            config.Model,
		responseFormat:   format,
		httpClient:       client,
		maxResponseBytes: maximum,
	}, nil
}

type requestBody struct {
	Model          string             `json:"model"`
	Messages       []provider.Message `json:"messages"`
	Temperature    float64            `json:"temperature"`
	MaxTokens      int                `json:"max_tokens,omitempty"`
	ResponseFormat any                `json:"response_format,omitempty"`
}

type responseBody struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage provider.Usage `json:"usage"`
}

func (c *Client) Complete(ctx context.Context, request provider.CompletionRequest) (provider.CompletionResponse, error) {
	body := requestBody{
		Model:       c.model,
		Messages:    request.Messages,
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
	}
	if request.Schema != nil {
		switch c.responseFormat {
		case "json_schema":
			var schema any
			if err := json.Unmarshal(request.Schema.Schema, &schema); err != nil {
				return provider.CompletionResponse{}, &provider.Error{Kind: "invalid_schema", Cause: err}
			}
			body.ResponseFormat = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   request.Schema.Name,
					"strict": request.Schema.Strict,
					"schema": schema,
				},
			}
		case "json_object":
			body.ResponseFormat = map[string]string{"type": "json_object"}
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return provider.CompletionResponse{}, &provider.Error{Kind: "request_encode", Cause: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return provider.CompletionResponse{}, &provider.Error{Kind: "request_create", Cause: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "rin/0.2")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return provider.CompletionResponse{}, ctx.Err()
		}
		return provider.CompletionResponse{}, &provider.Error{Kind: "transport", Retryable: true, Cause: err}
	}
	defer httpResponse.Body.Close()
	limited := io.LimitReader(httpResponse.Body, c.maxResponseBytes+1)
	responsePayload, err := io.ReadAll(limited)
	if err != nil {
		return provider.CompletionResponse{}, &provider.Error{Kind: "response_read", Retryable: true, Cause: err}
	}
	if int64(len(responsePayload)) > c.maxResponseBytes {
		return provider.CompletionResponse{}, &provider.Error{Kind: "response_too_large", Retryable: true}
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return provider.CompletionResponse{}, responseError(httpResponse, responsePayload)
	}
	var decoded responseBody
	if err := json.Unmarshal(responsePayload, &decoded); err != nil {
		return provider.CompletionResponse{}, &provider.Error{Kind: "response_decode", Retryable: true, Cause: err}
	}
	if len(decoded.Choices) == 0 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return provider.CompletionResponse{}, &provider.Error{Kind: "empty_completion", Retryable: true}
	}
	return provider.CompletionResponse{
		Content:      decoded.Choices[0].Message.Content,
		Model:        decoded.Model,
		FinishReason: decoded.Choices[0].FinishReason,
		Usage:        decoded.Usage,
	}, nil
}

func responseError(response *http.Response, payload []byte) error {
	code := "http_error"
	var decoded struct {
		Error struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &decoded) == nil && decoded.Error.Code != nil {
		code = fmt.Sprint(decoded.Error.Code)
	}
	retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusRequestTimeout || response.StatusCode >= 500
	return &provider.Error{
		Kind:       "http",
		Code:       code,
		StatusCode: response.StatusCode,
		Retryable:  retryable,
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
	}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if timestamp, err := http.ParseTime(value); err == nil && timestamp.After(now) {
		return timestamp.Sub(now)
	}
	return 0
}
