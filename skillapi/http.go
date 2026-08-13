package skillapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
)

const maxRequestBytes int64 = 128 << 10

type HTTPOptions struct {
	Token     string
	Principal host.Principal
}

type HTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPHandler(service *Service, options HTTPOptions) (http.Handler, error) {
	if service == nil || len(options.Token) < 32 {
		return nil, errors.New("skill service and a token of at least 32 bytes are required")
	}
	if err := host.ValidatePrincipal(options.Principal); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /skills/v1/list", skillHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeInput[ListInput](body)
		if err != nil {
			return nil, err
		}
		return service.List(ctx, options.Principal, input)
	}))
	mux.HandleFunc("POST /skills/v1/get", skillHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeInput[GetInput](body)
		if err != nil {
			return nil, err
		}
		return service.Get(ctx, options.Principal, input)
	}))
	mux.HandleFunc("POST /skills/v1/save", skillHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeInput[SaveInput](body)
		if err != nil {
			return nil, err
		}
		return service.Save(ctx, options.Principal, input)
	}))
	mux.HandleFunc("POST /skills/v1/reload", skillHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		if err := requireEmptyObject(body); err != nil {
			return nil, err
		}
		return service.Reload(ctx, options.Principal)
	}))
	return mux, nil
}

func NewHTTPClient(baseURL, token string) (*HTTPClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || len(token) < 32 {
		return nil, errors.New("skill client requires a loopback HTTP URL and token")
	}
	hostname := parsed.Hostname()
	if hostname != "localhost" && hostname != "127.0.0.1" && hostname != "::1" {
		return nil, errors.New("skill client URL must use loopback")
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(parsed.String(), "/"), token: token,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (client *HTTPClient) List(ctx context.Context, input ListInput) (ListOutput, error) {
	var output ListOutput
	err := client.post(ctx, "/skills/v1/list", input, &output)
	return output, err
}

func (client *HTTPClient) Get(ctx context.Context, input GetInput) (GetOutput, error) {
	var output GetOutput
	err := client.post(ctx, "/skills/v1/get", input, &output)
	return output, err
}

func (client *HTTPClient) Save(ctx context.Context, input SaveInput) (GetOutput, error) {
	var output GetOutput
	err := client.post(ctx, "/skills/v1/save", input, &output)
	return output, err
}

func (client *HTTPClient) Reload(ctx context.Context) (ReloadOutput, error) {
	var output ReloadOutput
	err := client.post(ctx, "/skills/v1/reload", struct{}{}, &output)
	return output, err
}

func (client *HTTPClient) post(ctx context.Context, path string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("skill service returned status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func skillHTTPHandler(
	options HTTPOptions,
	call func(context.Context, []byte) (any, error),
) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(options.Token)) != 1 {
			writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
		if err != nil || int64(len(body)) > maxRequestBytes {
			writeHTTPError(response, http.StatusBadRequest, "invalid_request")
			return
		}
		output, err := call(request.Context(), body)
		if err != nil {
			status := http.StatusBadRequest
			code := "invalid_request"
			if errors.Is(err, ErrForbidden) {
				status, code = http.StatusForbidden, "forbidden"
			} else if errors.Is(err, cognition.ErrProviderNotFound) {
				status, code = http.StatusNotFound, "not_found"
			} else if errors.Is(err, cognition.ErrProviderConflict) {
				status, code = http.StatusConflict, "conflict"
			}
			writeHTTPError(response, status, code)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(output)
	}
}

func decodeInput[T any](payload []byte) (T, error) {
	var target T
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return target, ErrInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return target, ErrInvalid
	}
	return target, nil
}

func requireEmptyObject(payload []byte) error {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&value); err != nil || len(value) != 0 {
		return ErrInvalid
	}
	return nil
}

func writeHTTPError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{"error": map[string]string{"code": code}})
}
