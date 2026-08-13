package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/release"
)

const maxEmbeddingDimensions = 8_192

type EmbeddingConfig struct {
	BaseURL          string
	APIKey           string
	Model            string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type EmbeddingClient struct {
	endpoint         string
	apiKey           string
	model            string
	httpClient       *http.Client
	maxResponseBytes int64
}

func NewEmbeddingClient(config EmbeddingConfig) (*EmbeddingClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("embedding base URL must be an http(s) URL without user information")
	}
	model := strings.TrimSpace(config.Model)
	if model == "" || len(model) > 200 {
		return nil, errors.New("embedding model is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	maximum := config.MaxResponseBytes
	if maximum <= 0 {
		maximum = 8 << 20
	}
	return &EmbeddingClient{
		endpoint: strings.TrimRight(parsed.String(), "/") + "/embeddings",
		apiKey:   strings.TrimSpace(config.APIKey), model: model, httpClient: &copy,
		maxResponseBytes: maximum,
	}, nil
}

func (client *EmbeddingClient) Embed(
	ctx context.Context,
	request provider.EmbeddingRequest,
) (provider.EmbeddingResponse, error) {
	if ctx == nil || len(request.Inputs) == 0 || len(request.Inputs) > 64 {
		return provider.EmbeddingResponse{}, errors.New("embedding request is invalid")
	}
	inputs := make([]string, len(request.Inputs))
	for index, input := range request.Inputs {
		inputs[index] = strings.TrimSpace(input)
		if inputs[index] == "" || len(inputs[index]) > 32_000 || strings.ContainsRune(inputs[index], 0) {
			return provider.EmbeddingResponse{}, errors.New("embedding input is invalid")
		}
	}
	payload, err := json.Marshal(map[string]any{"model": client.model, "input": inputs})
	if err != nil {
		return provider.EmbeddingResponse{}, &provider.Error{Kind: "request_encode", Cause: err}
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(payload))
	if err != nil {
		return provider.EmbeddingResponse{}, &provider.Error{Kind: "request_create", Cause: err}
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "rin/"+release.Version)
	if client.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return provider.EmbeddingResponse{}, ctx.Err()
		}
		return provider.EmbeddingResponse{}, &provider.Error{Kind: "transport", Retryable: true, Cause: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil || int64(len(body)) > client.maxResponseBytes {
		return provider.EmbeddingResponse{}, &provider.Error{Kind: "response_read", Retryable: true, ProviderReached: true, Cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return provider.EmbeddingResponse{}, responseError(response, body)
	}
	if !jsonwire.Valid(body) {
		return provider.EmbeddingResponse{}, &provider.Error{Kind: "response_decode", ProviderReached: true}
	}
	var decoded struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage provider.Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.Data) != len(inputs) {
		return provider.EmbeddingResponse{}, &provider.Error{Kind: "response_decode", ProviderReached: true, Cause: err}
	}
	sort.Slice(decoded.Data, func(left, right int) bool {
		return decoded.Data[left].Index < decoded.Data[right].Index
	})
	vectors := make([][]float32, len(decoded.Data))
	dimensions := 0
	for index, item := range decoded.Data {
		if item.Index != index || len(item.Embedding) == 0 || len(item.Embedding) > maxEmbeddingDimensions ||
			(dimensions != 0 && len(item.Embedding) != dimensions) {
			return provider.EmbeddingResponse{}, &provider.Error{Kind: "invalid_embedding", ProviderReached: true}
		}
		dimensions = len(item.Embedding)
		vector := append([]float32(nil), item.Embedding...)
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return provider.EmbeddingResponse{}, &provider.Error{Kind: "invalid_embedding", ProviderReached: true}
			}
		}
		vectors[index] = vector
	}
	model := strings.TrimSpace(decoded.Model)
	if model == "" {
		model = client.model
	}
	return provider.EmbeddingResponse{Model: model, Embeddings: vectors, Usage: decoded.Usage}, nil
}

var _ provider.EmbeddingProvider = (*EmbeddingClient)(nil)
