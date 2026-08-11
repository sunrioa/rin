package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
)

const (
	defaultHTTPClientTimeout          = 30 * time.Second
	defaultHTTPClientMaxResponseBytes = int64(8 << 20)
)

// HTTPClient connects task callers to the loopback Rin daemon. The daemon,
// not the request body, owns the Principal used by every call.
type HTTPClient struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

func NewHTTPClient(baseURL, token string) (*HTTPClient, error) {
	parsed, err := validateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if len(token) < 32 {
		return nil, fmt.Errorf("%w: token must contain at least 32 bytes", ErrInvalid)
	}
	return &HTTPClient{
		baseURL: parsed, token: token,
		client: &http.Client{
			Timeout: defaultHTTPClientTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *HTTPClient) Info(ctx context.Context) (ClientInfo, error) {
	var info ClientInfo
	if err := client.request(ctx, http.MethodGet, "info", nil, &info); err != nil {
		return ClientInfo{}, err
	}
	if info.ContractVersion != ContractVersion {
		return ClientInfo{}, fmt.Errorf("%w: unsupported Agent contract %q", ErrInvalid, info.ContractVersion)
	}
	if err := host.ValidatePrincipal(info.Principal); err != nil || !principalHasTaskScope(info.Principal) {
		return ClientInfo{}, fmt.Errorf("%w: invalid daemon task principal", ErrInvalid)
	}
	info.Principal = cloneTaskPrincipal(info.Principal)
	return info, nil
}

func (client *HTTPClient) StartTask(
	ctx context.Context,
	input cognition.StartTaskInput,
) (TaskDispatch, error) {
	var dispatch TaskDispatch
	err := client.request(ctx, http.MethodPost, "tasks/start", input, &dispatch)
	return dispatch, err
}

func (client *HTTPClient) GetTask(
	ctx context.Context,
	taskID string,
) (cognition.TaskSession, error) {
	var task cognition.TaskSession
	err := client.request(ctx, http.MethodPost, "tasks/get", TaskTarget{TaskID: taskID}, &task)
	return task, err
}

func (client *HTTPClient) RunTask(
	ctx context.Context,
	taskID string,
) (TaskDispatch, error) {
	return client.dispatchTarget(ctx, "tasks/run", taskID)
}

func (client *HTTPClient) ResumeTask(
	ctx context.Context,
	taskID string,
) (TaskDispatch, error) {
	return client.dispatchTarget(ctx, "tasks/resume", taskID)
}

func (client *HTTPClient) CancelTask(
	ctx context.Context,
	taskID string,
) (TaskDispatch, error) {
	return client.dispatchTarget(ctx, "tasks/cancel", taskID)
}

func (client *HTTPClient) dispatchTarget(
	ctx context.Context,
	action, taskID string,
) (TaskDispatch, error) {
	var dispatch TaskDispatch
	err := client.request(ctx, http.MethodPost, action, TaskTarget{TaskID: taskID}, &dispatch)
	return dispatch, err
}

func (client *HTTPClient) request(
	ctx context.Context,
	method, action string,
	input, output any,
) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("%w: encode request: %v", ErrInvalid, err)
		}
		body = bytes.NewReader(payload)
	}
	endpoint := *client.baseURL
	endpoint.Path = "/agent/v1/" + action
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrInvalid, err)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: Agent Daemon request: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, defaultHTTPClientMaxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w: read Agent Daemon response: %v", ErrUnavailable, err)
	}
	if int64(len(payload)) > defaultHTTPClientMaxResponseBytes {
		return fmt.Errorf("%w: Agent Daemon response is too large", ErrUnavailable)
	}
	contentType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentTypeErr != nil || contentType != "application/json" || !jsonwire.Valid(payload) {
		return fmt.Errorf("%w: Agent Daemon returned invalid JSON", ErrUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var remote errorResponse
		if err := decodeClientJSON(payload, &remote); err != nil {
			return fmt.Errorf("%w: Agent Daemon returned HTTP %d", ErrUnavailable, response.StatusCode)
		}
		return clientStatusError(response.StatusCode, remote.Code, remote.Error)
	}
	if err := decodeClientJSON(payload, output); err != nil {
		return fmt.Errorf("%w: decode Agent Daemon response: %v", ErrUnavailable, err)
	}
	return nil
}

func validateBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("%w: Agent Daemon URL must be a plain loopback HTTP origin", ErrInvalid)
	}
	hostName, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || port == "" {
		return nil, fmt.Errorf("%w: Agent Daemon URL requires an explicit port", ErrInvalid)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65_535 {
		return nil, fmt.Errorf("%w: Agent Daemon URL has an invalid port", ErrInvalid)
	}
	if !strings.EqualFold(hostName, "localhost") {
		ip := net.ParseIP(hostName)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("%w: Agent Daemon URL must use a loopback host", ErrInvalid)
		}
	}
	parsed.Path = ""
	return parsed, nil
}

func decodeClientJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("response must contain one JSON value")
	}
	return nil
}

func clientStatusError(status int, code, message string) error {
	if message == "" {
		message = http.StatusText(status)
	}
	var target error
	switch code {
	case "invalid":
		target = ErrInvalid
	case "not_found":
		target = ErrNotFound
	case "forbidden":
		target = ErrForbidden
	case "conflict":
		target = ErrConflict
	case "capacity":
		target = ErrCapacity
	case "unavailable":
		target = ErrUnavailable
	default:
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			target = ErrForbidden
		} else {
			target = ErrUnavailable
		}
	}
	return fmt.Errorf("%w: %s", target, message)
}

var _ TaskClient = (*HTTPClient)(nil)
