package openai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/provider/openai"
)

func TestEmbeddingClientUsesOpenAICompatibleContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/embeddings" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request = %s auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"model":"embed-test","data":[{"index":0,"embedding":[0.5,0.5]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer server.Close()
	client, err := openai.NewEmbeddingClient(openai.EmbeddingConfig{
		BaseURL: server.URL + "/v1", APIKey: "secret", Model: "embed-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Embed(context.Background(), provider.EmbeddingRequest{Inputs: []string{"hello"}})
	if err != nil || len(result.Embeddings) != 1 || len(result.Embeddings[0]) != 2 || result.Usage.TotalTokens != 3 {
		t.Fatalf("embedding = %#v, %v", result, err)
	}
}

func TestEmbeddingClientRejectsMalformedVectors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"data":[{"index":0,"embedding":[]}],"usage":{}}`))
	}))
	defer server.Close()
	client, _ := openai.NewEmbeddingClient(openai.EmbeddingConfig{BaseURL: server.URL, Model: "embed-test"})
	if _, err := client.Embed(context.Background(), provider.EmbeddingRequest{Inputs: []string{"hello"}}); err == nil {
		t.Fatal("empty embedding was accepted")
	}
}

func TestEmbeddingClientClassifiesTransientHTTPFailures(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.WriteHeader(status)
				_, _ = response.Write([]byte(`{"error":{"code":"temporary"}}`))
			}))
			defer server.Close()
			client, err := openai.NewEmbeddingClient(openai.EmbeddingConfig{
				BaseURL: server.URL, Model: "embed-test",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Embed(context.Background(), provider.EmbeddingRequest{Inputs: []string{"hello"}})
			var providerError *provider.Error
			if !errors.As(err, &providerError) || !providerError.Retryable ||
				providerError.StatusCode != status || !providerError.ProviderReached {
				t.Fatalf("status %d error = %#v", status, err)
			}
		})
	}
}
