// Package httpjson implements the shared JSON transport boundary. Services keep
// ownership of authorization policy and their public error envelopes.
package httpjson

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/sunrioa/rin/internal/jsonwire"
)

// Authorized checks a Bearer credential without interpreting service scopes.
func Authorized(request *http.Request, token string) bool {
	provided, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && len(provided) == len(token) &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

// ReadBody requires JSON and bounds the read before allocating the full body.
func ReadBody(response http.ResponseWriter, request *http.Request, limit int64) ([]byte, error) {
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return nil, errors.New("content type must be application/json")
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, errors.New("request body exceeds the configured limit")
	}
	return payload, nil
}

// Decode rejects ambiguous JSON, malformed Unicode and unknown fields.
func Decode(payload []byte, target any) error {
	if err := jsonwire.Validate(payload); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func DecodeRequest(response http.ResponseWriter, request *http.Request, limit int64, target any) error {
	payload, err := ReadBody(response, request, limit)
	if err != nil {
		return err
	}
	return Decode(payload, target)
}

// Write encodes before committing the status so encoding failures cannot look
// like successful responses.
func Write(response http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(response, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(append(payload, '\n'))
}
