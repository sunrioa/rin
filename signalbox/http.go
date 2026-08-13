package signalbox

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxHTTPBodyBytes int64 = 1 << 20

type HTTPOptions struct {
	Token string
}

type HTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPHandler(service *Service, options HTTPOptions) (http.Handler, error) {
	if service == nil || len(options.Token) < 32 {
		return nil, ErrInvalid
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /signals/v1/host/settings", signalHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[HostSettingsInput](body)
		if err != nil {
			return nil, err
		}
		return service.ConfigureHost(ctx, input)
	}))
	mux.HandleFunc("POST /signals/v1/host/publish", signalHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[HostPublishInput](body)
		if err != nil {
			return nil, err
		}
		return service.PublishHost(ctx, input)
	}))
	mux.HandleFunc("POST /signals/v1/list", signalHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[ListInput](body)
		if err != nil {
			return nil, err
		}
		return service.List(ctx, input)
	}))
	mux.HandleFunc("POST /signals/v1/wait", signalHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[WaitInput](body)
		if err != nil {
			return nil, err
		}
		return service.Wait(ctx, input)
	}))
	return mux, nil
}

func NewHTTPClient(baseURL, token string) (*HTTPClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || len(token) < 32 {
		return nil, ErrInvalid
	}
	hostname := parsed.Hostname()
	if hostname != "localhost" && hostname != "127.0.0.1" && hostname != "::1" {
		return nil, ErrInvalid
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(parsed.String(), "/"), token: token,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (client *HTTPClient) ConfigureHost(ctx context.Context, input HostSettingsInput) (Settings, error) {
	var output Settings
	err := client.post(ctx, "/signals/v1/host/settings", input, &output)
	return output, err
}

func (client *HTTPClient) PublishHost(ctx context.Context, input HostPublishInput) (PublishResult, error) {
	var output PublishResult
	err := client.post(ctx, "/signals/v1/host/publish", input, &output)
	return output, err
}

func (client *HTTPClient) List(ctx context.Context, input ListInput) (Page, error) {
	var output Page
	err := client.post(ctx, "/signals/v1/list", input, &output)
	return output, err
}

func (client *HTTPClient) Wait(ctx context.Context, input WaitInput) (Update, error) {
	var output Update
	err := client.post(ctx, "/signals/v1/wait", input, &output)
	return output, err
}

func (client *HTTPClient) post(ctx context.Context, path string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPBodyBytes+1))
	if err != nil || int64(len(body)) > maxHTTPBodyBytes {
		return ErrInvalid
	}
	if response.StatusCode != http.StatusOK {
		return decodeHTTPError(body)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func signalHTTPHandler(
	options HTTPOptions,
	call func(context.Context, []byte) (any, error),
) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if len(token) != len(options.Token) || subtle.ConstantTimeCompare([]byte(token), []byte(options.Token)) != 1 {
			writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxHTTPBodyBytes+1))
		if err != nil || int64(len(body)) > maxHTTPBodyBytes {
			writeHTTPError(response, http.StatusBadRequest, "invalid_request")
			return
		}
		output, err := call(request.Context(), body)
		if err != nil {
			status, code := httpError(err)
			writeHTTPError(response, status, code)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(output)
	}
}

func decodeHTTPInput[T any](payload []byte) (T, error) {
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

func httpError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, ErrClosed):
		return http.StatusServiceUnavailable, "unavailable"
	default:
		return http.StatusBadRequest, "invalid_request"
	}
}

func writeHTTPError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"code": code, "error": code})
}

func decodeHTTPError(payload []byte) error {
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(payload, &body)
	switch body.Code {
	case "forbidden":
		return ErrForbidden
	case "not_found":
		return ErrNotFound
	case "unavailable":
		return ErrClosed
	default:
		return ErrInvalid
	}
}
