package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

var positiveIntEnvironment = []string{
	"RIN_TRANSFER_MAX_CONCURRENT",
	"RIN_JOB_WORKERS",
	"RIN_JOB_QUEUE_SIZE",
	"RIN_JOB_MAX_RETAINED",
	"RIN_GENERATION_WORKERS",
	"RIN_GENERATION_QUEUE_SIZE",
	"RIN_GENERATION_MAX_RETAINED",
	"RIN_GENERATION_CACHE_ENTRIES",
	"RIN_GENERATION_MAX_OUTPUT_BYTES",
	"RIN_MODEL_MAX_ATTEMPTS",
	"RIN_MODEL_BREAKER_FAILURES",
	"RIN_MODEL_CACHE_ENTRIES",
}

var positiveUintEnvironment = []string{
	"RIN_SESSION_STATE_MAX_BYTES",
	"RIN_TRANSFER_MAX_BYTES",
	"RIN_TRANSFER_MAX_EVENTS",
	"RIN_GENERATION_MAX_RETAINED_BYTES",
}

var nonNegativeUintEnvironment = []string{
	"RIN_SESSION_SOFT_LIMIT_BYTES",
	"RIN_SESSION_HARD_LIMIT_BYTES",
}

var positiveDurationEnvironment = []string{
	"RIN_REQUEST_TIMEOUT",
	"RIN_TRANSFER_TIMEOUT",
	"RIN_TRANSFER_INACTIVITY_TIMEOUT",
	"RIN_JOB_TTL",
	"RIN_GENERATION_JOB_TTL",
	"RIN_GENERATION_CACHE_TTL",
	"RIN_GENERATION_CLEANUP_INTERVAL",
	"RIN_MODEL_ATTEMPT_TIMEOUT",
	"RIN_MODEL_TOTAL_TIMEOUT",
	"RIN_MODEL_INITIAL_BACKOFF",
	"RIN_MODEL_MAX_BACKOFF",
	"RIN_MODEL_BREAKER_OPEN",
	"RIN_MODEL_CACHE_TTL",
}

func validateServeEnvironment() error {
	if value := os.Getenv("RIN_MAX_BODY_BYTES"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return invalidEnvironment(
				"RIN_MAX_BODY_BYTES",
				"must be a positive base-10 integer",
			)
		}
	}
	for _, key := range positiveIntEnvironment {
		value := os.Getenv(key)
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return invalidEnvironment(
				key,
				"must be a positive base-10 integer",
			)
		}
	}
	for _, key := range positiveUintEnvironment {
		if err := validateUintEnvironment(key, false); err != nil {
			return err
		}
	}
	for _, key := range nonNegativeUintEnvironment {
		if err := validateUintEnvironment(key, true); err != nil {
			return err
		}
	}
	for _, key := range positiveDurationEnvironment {
		value := os.Getenv(key)
		if value == "" {
			continue
		}
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return invalidEnvironment(
				key,
				"must be a positive Go duration",
			)
		}
	}
	if value := os.Getenv("RIN_MODEL_ALLOW_INSECURE"); value != "" {
		if _, err := strconv.ParseBool(value); err != nil {
			return invalidEnvironment(
				"RIN_MODEL_ALLOW_INSECURE",
				"must be a boolean",
			)
		}
	}
	if token := os.Getenv("RIN_TOKEN"); strings.ContainsAny(token, " \t\r\n") {
		return invalidEnvironment(
			"RIN_TOKEN",
			"must not contain whitespace",
		)
	}
	return nil
}

func validateUintEnvironment(key string, allowZero bool) error {
	value := os.Getenv(key)
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || (!allowZero && parsed == 0) {
		requirement := "must be a positive base-10 integer"
		if allowZero {
			requirement = "must be a non-negative base-10 integer"
		}
		return invalidEnvironment(key, requirement)
	}
	return nil
}

func invalidEnvironment(key, requirement string) error {
	return fmt.Errorf("invalid %s: %s", key, requirement)
}

type serveConfiguration struct {
	maxBodyBytes              int64
	sessionSoftLimitBytes     uint64
	sessionHardLimitBytes     uint64
	maxSessionStateBytes      uint64
	maxTransferBytes          uint64
	maxTransferEvents         uint64
	maxConcurrentTransfers    int
	requestTimeout            time.Duration
	transferTimeout           time.Duration
	transferInactivityTimeout time.Duration
}

func validateServeConfiguration(config serveConfiguration) error {
	switch {
	case config.maxBodyBytes <= 0:
		return errors.New("max-body-bytes must be positive")
	case config.sessionHardLimitBytes > 0 &&
		config.sessionSoftLimitBytes > config.sessionHardLimitBytes:
		return errors.New(
			"session-soft-limit-bytes must not exceed session-hard-limit-bytes",
		)
	case config.maxSessionStateBytes == 0:
		return errors.New("session-state-max-bytes must be positive")
	case config.maxTransferBytes == 0:
		return errors.New("transfer-max-bytes must be positive")
	case config.maxTransferEvents == 0:
		return errors.New("transfer-max-events must be positive")
	case config.maxConcurrentTransfers <= 0:
		return errors.New("transfer-max-concurrent must be positive")
	case config.requestTimeout <= 0:
		return errors.New("request-timeout must be positive")
	case config.transferTimeout <= 0:
		return errors.New("transfer-timeout must be positive")
	case config.transferInactivityTimeout <= 0:
		return errors.New("transfer-inactivity-timeout must be positive")
	default:
		return nil
	}
}
