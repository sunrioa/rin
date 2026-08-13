package taskstate

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

	"github.com/sunrioa/rin/controlplane"
)

const maxHTTPRequestBytes int64 = 1 << 20

type GetPlanInput struct {
	PlanID string `json:"plan_id"`
}

type HTTPOptions struct {
	Token string
}

type HTTPClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPHandler(client PlanClient, options HTTPOptions) (http.Handler, error) {
	if client == nil || len(options.Token) < 32 {
		return nil, invalid("http", "requires a plan client and token of at least 32 bytes")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plans/v1/create", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[Draft](body)
		if err != nil {
			return nil, err
		}
		return client.CreatePlan(ctx, input)
	}))
	mux.HandleFunc("POST /plans/v1/get", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[GetPlanInput](body)
		if err != nil {
			return nil, err
		}
		return client.GetPlan(ctx, input.PlanID)
	}))
	mux.HandleFunc("POST /plans/v1/wait", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[WaitInput](body)
		if err != nil {
			return nil, err
		}
		return client.WaitPlan(ctx, input)
	}))
	mux.HandleFunc("POST /plans/v1/revise", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[ReviseInput](body)
		if err != nil {
			return nil, err
		}
		return client.RevisePlan(ctx, input)
	}))
	mux.HandleFunc("POST /plans/v1/status", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[StatusInput](body)
		if err != nil {
			return nil, err
		}
		return client.SetPlanStatus(ctx, input)
	}))
	mux.HandleFunc("POST /plans/v1/transition", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[TransitionInput](body)
		if err != nil {
			return nil, err
		}
		return client.RequestTransition(ctx, input)
	}))
	mux.HandleFunc("POST /plans/v1/submit-step-action", planHTTPHandler(options, func(ctx context.Context, body []byte) (any, error) {
		input, err := decodeHTTPInput[SubmitStepActionInput](body)
		if err != nil {
			return nil, err
		}
		return client.SubmitStepAction(ctx, input)
	}))
	return mux, nil
}

func NewHTTPClient(baseURL, token string) (*HTTPClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || len(token) < 32 {
		return nil, invalid("plan client", "requires a loopback HTTP URL and token")
	}
	hostname := parsed.Hostname()
	if hostname != "localhost" && hostname != "127.0.0.1" && hostname != "::1" {
		return nil, invalid("plan client", "URL must use loopback")
	}
	return &HTTPClient{
		baseURL: strings.TrimRight(parsed.String(), "/"), token: token,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (client *HTTPClient) CreatePlan(ctx context.Context, input Draft) (PlanState, error) {
	var output PlanState
	err := client.post(ctx, "/plans/v1/create", input, &output)
	return output, err
}

func (client *HTTPClient) GetPlan(ctx context.Context, planID string) (PlanState, error) {
	var output PlanState
	err := client.post(ctx, "/plans/v1/get", GetPlanInput{PlanID: planID}, &output)
	return output, err
}

func (client *HTTPClient) WaitPlan(ctx context.Context, input WaitInput) (PlanUpdate, error) {
	var output PlanUpdate
	err := client.post(ctx, "/plans/v1/wait", input, &output)
	return output, err
}

func (client *HTTPClient) RevisePlan(ctx context.Context, input ReviseInput) (PlanState, error) {
	var output PlanState
	err := client.post(ctx, "/plans/v1/revise", input, &output)
	return output, err
}

func (client *HTTPClient) SetPlanStatus(ctx context.Context, input StatusInput) (PlanState, error) {
	var output PlanState
	err := client.post(ctx, "/plans/v1/status", input, &output)
	return output, err
}

func (client *HTTPClient) RequestTransition(ctx context.Context, input TransitionInput) (PlanState, error) {
	var output PlanState
	err := client.post(ctx, "/plans/v1/transition", input, &output)
	return output, err
}

func (client *HTTPClient) SubmitStepAction(
	ctx context.Context,
	input SubmitStepActionInput,
) (controlplane.OperationView, error) {
	var output controlplane.OperationView
	err := client.post(ctx, "/plans/v1/submit-step-action", input, &output)
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
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPRequestBytes+1))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return decodeHTTPError(response.StatusCode, body)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	return nil
}

func planHTTPHandler(
	options HTTPOptions,
	call func(context.Context, []byte) (any, error),
) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if len(token) != len(options.Token) ||
			subtle.ConstantTimeCompare([]byte(token), []byte(options.Token)) != 1 {
			writePlanHTTPError(response, http.StatusUnauthorized, "unauthorized")
			return
		}
		body, err := io.ReadAll(io.LimitReader(request.Body, maxHTTPRequestBytes+1))
		if err != nil || int64(len(body)) > maxHTTPRequestBytes {
			writePlanHTTPError(response, http.StatusBadRequest, "invalid_request")
			return
		}
		output, err := call(request.Context(), body)
		if err != nil {
			status, code := planHTTPError(err)
			writePlanHTTPError(response, status, code)
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

func planHTTPError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, ErrConflict):
		return http.StatusConflict, "conflict"
	case errors.Is(err, ErrCapacity):
		return http.StatusTooManyRequests, "capacity"
	default:
		return http.StatusBadRequest, "invalid_request"
	}
}

func writePlanHTTPError(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{
		"code": code, "error": code,
	})
}

func decodeHTTPError(status int, payload []byte) error {
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(payload, &envelope)
	switch envelope.Code {
	case "forbidden":
		return ErrForbidden
	case "not_found":
		return ErrNotFound
	case "conflict":
		return ErrConflict
	case "capacity":
		return ErrCapacity
	default:
		return fmt.Errorf("plan service returned status %d", status)
	}
}

var _ PlanClient = (*HTTPClient)(nil)
