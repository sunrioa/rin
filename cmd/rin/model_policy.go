package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/provider/openai"
	rinruntime "github.com/sunrioa/rin/runtime"
)

type modelRuntime struct {
	Policy           rinruntime.Policy
	Mode             string
	GenerationClient provider.Client
}

func buildPolicy(logger *slog.Logger) (rinruntime.Policy, string, error) {
	runtime, err := buildModelRuntime(logger)
	if err != nil {
		return nil, "", err
	}
	return runtime.Policy, runtime.Mode, nil
}

func buildModelRuntime(logger *slog.Logger) (modelRuntime, error) {
	mode := strings.ToLower(envOr("RIN_POLICY", "deterministic"))
	if mode == "deterministic" {
		return modelRuntime{Policy: policy.Deterministic{}, Mode: "deterministic"}, nil
	}
	if mode != "model" {
		return modelRuntime{}, errors.New("RIN_POLICY must be deterministic or model")
	}
	baseURL := os.Getenv("RIN_MODEL_BASE_URL")
	model := os.Getenv("RIN_MODEL")
	if baseURL == "" || model == "" {
		return modelRuntime{}, errors.New("model policy requires RIN_MODEL_BASE_URL and RIN_MODEL")
	}
	if err := validateModelEndpoint(baseURL, envBool("RIN_MODEL_ALLOW_INSECURE", false)); err != nil {
		return modelRuntime{}, err
	}
	parsed, _ := url.Parse(baseURL)
	if !isLoopbackHost(parsed.Hostname()) && os.Getenv("RIN_MODEL_API_KEY") == "" {
		return modelRuntime{}, errors.New("remote model endpoint requires RIN_MODEL_API_KEY")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: envDuration("RIN_MODEL_ATTEMPT_TIMEOUT", 15*time.Second),
	}
	client, err := openai.New(openai.Config{
		BaseURL: baseURL, APIKey: os.Getenv("RIN_MODEL_API_KEY"), Model: model,
		ResponseFormat: envOr("RIN_MODEL_RESPONSE_FORMAT", "json_schema"),
		HTTPClient:     &http.Client{Transport: transport},
	})
	if err != nil {
		return modelRuntime{}, err
	}
	resilient, err := provider.NewResilient(client, provider.ResilienceConfig{
		MaxAttempts:      envInt("RIN_MODEL_MAX_ATTEMPTS", 2),
		AttemptTimeout:   envDuration("RIN_MODEL_ATTEMPT_TIMEOUT", 15*time.Second),
		TotalTimeout:     envDuration("RIN_MODEL_TOTAL_TIMEOUT", 25*time.Second),
		InitialBackoff:   envDuration("RIN_MODEL_INITIAL_BACKOFF", 150*time.Millisecond),
		MaxBackoff:       envDuration("RIN_MODEL_MAX_BACKOFF", 2*time.Second),
		FailureThreshold: envInt("RIN_MODEL_BREAKER_FAILURES", 3),
		OpenDuration:     envDuration("RIN_MODEL_BREAKER_OPEN", 20*time.Second),
	})
	if err != nil {
		return modelRuntime{}, err
	}
	modelPolicy := policy.Model{Client: resilient}
	cached, err := policy.NewCached(modelPolicy, policy.CacheConfig{
		MaxEntries: envInt("RIN_MODEL_CACHE_ENTRIES", 256),
		TTL:        envDuration("RIN_MODEL_CACHE_TTL", 10*time.Minute),
	})
	if err != nil {
		return modelRuntime{}, err
	}
	selectedPolicy := policy.Failover{
		Primary: cached, Fallback: policy.Deterministic{},
		OnFallback: func(err error) {
			logger.Warn("model policy used deterministic fallback", "error", err)
		},
	}
	return modelRuntime{
		Policy: selectedPolicy, Mode: "model-with-fallback", GenerationClient: resilient,
	}, nil
}

func validateModelEndpoint(raw string, allowInsecure bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("RIN_MODEL_BASE_URL must be an http(s) URL without user information")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme != "http" {
		return errors.New("RIN_MODEL_BASE_URL must use http or https")
	}
	if isLoopbackHost(parsed.Hostname()) || allowInsecure {
		return nil
	}
	return errors.New("non-loopback model endpoint must use https unless RIN_MODEL_ALLOW_INSECURE=true")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func describeModelConfig() string {
	return fmt.Sprintf("model=%s response_format=%s", os.Getenv("RIN_MODEL"), envOr("RIN_MODEL_RESPONSE_FORMAT", "json_schema"))
}
