package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/sunrioa/rin/generation"
	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/jobs"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

var version = "0.7.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rin:", err)
		os.Exit(1)
	}
}

func run(arguments []string) (resultErr error) {
	if len(arguments) > 0 && arguments[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if len(arguments) > 0 && arguments[0] == "inspect" {
		return runInspect(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && arguments[0] == "init" {
		return runInit(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && arguments[0] == "add" {
		return runAdd(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && arguments[0] == "conformance" {
		return runConformance(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && arguments[0] == "doctor" {
		return runDoctor(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && arguments[0] == "serve" {
		arguments = arguments[1:]
	}
	flags := flag.NewFlagSet("rin serve", flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprint(flags.Output(), `Usage:
  rin init host [options]
  rin add skill [options]
  rin conformance host [options]
  rin doctor host [options]
  rin inspect [options]
  rin serve [options]
  rin version

Run "rin init host --help" for the offline Host generator.

Serve options:
`)
		flags.PrintDefaults()
	}
	address := flags.String("addr", envOr("RIN_ADDR", "127.0.0.1:7374"), "listen address")
	dataDirectory := flags.String("data", envOr("RIN_DATA_DIR", "./rin-data"), "event and snapshot directory")
	allowRemote := flags.Bool("allow-remote", false, "allow a non-loopback listen address")
	maxBody := flags.Int64("max-body-bytes", envInt64("RIN_MAX_BODY_BYTES", httpapi.DefaultMaxBodyBytes), "maximum JSON request size")
	sessionSoftLimit := flags.Uint64(
		"session-soft-limit-bytes",
		envUint64("RIN_SESSION_SOFT_LIMIT_BYTES", 0),
		"per-Session managed byte warning threshold; 0 disables",
	)
	sessionHardLimit := flags.Uint64(
		"session-hard-limit-bytes",
		envUint64("RIN_SESSION_HARD_LIMIT_BYTES", 0),
		"per-Session managed byte hard limit; 0 disables",
	)
	maxSessionStateBytes := flags.Uint64(
		"session-state-max-bytes",
		envUint64(
			"RIN_SESSION_STATE_MAX_BYTES",
			rinruntime.DefaultMaxSessionStateBytes,
		),
		"maximum compact JSON bytes in one Session State",
	)
	maxTransferBytes := flags.Uint64(
		"transfer-max-bytes",
		envUint64("RIN_TRANSFER_MAX_BYTES", rinruntime.DefaultMaxTransferBytes),
		"maximum bytes in one Session Transfer",
	)
	maxTransferEvents := flags.Uint64(
		"transfer-max-events",
		envUint64("RIN_TRANSFER_MAX_EVENTS", rinruntime.DefaultMaxTransferEvents),
		"maximum events in one Session Transfer",
	)
	maxConcurrentTransfers := flags.Int(
		"transfer-max-concurrent",
		envInt(
			"RIN_TRANSFER_MAX_CONCURRENT",
			rinruntime.DefaultMaxConcurrentTransfers,
		),
		"maximum concurrent Session Transfers",
	)
	requestTimeout := flags.Duration(
		"request-timeout",
		envDuration("RIN_REQUEST_TIMEOUT", httpapi.DefaultRequestTimeout),
		"overall timeout for ordinary API requests",
	)
	transferTimeout := flags.Duration(
		"transfer-timeout",
		envDuration("RIN_TRANSFER_TIMEOUT", httpapi.DefaultTransferTimeout),
		"overall timeout for Session Transfer requests",
	)
	transferInactivityTimeout := flags.Duration(
		"transfer-inactivity-timeout",
		envDuration(
			"RIN_TRANSFER_INACTIVITY_TIMEOUT",
			httpapi.DefaultTransferInactivityTimeout,
		),
		"rolling read/write inactivity timeout for Session Transfers",
	)
	scrubInterval := flags.Duration(
		"scrub-interval",
		envDuration("RIN_SCRUB_INTERVAL", 15*time.Minute),
		"interval between bounded event-log scrub passes",
	)
	scrubTimeout := flags.Duration(
		"scrub-timeout",
		envDuration("RIN_SCRUB_TIMEOUT", 30*time.Second),
		"timeout for one bounded event-log scrub pass",
	)
	scrubMaxEvents := flags.Int(
		"scrub-max-events",
		envInt("RIN_SCRUB_MAX_EVENTS", rinruntime.DefaultScrubEventBudget),
		"maximum events verified by one scrub pass",
	)
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if err := validateServeEnvironment(); err != nil {
		return err
	}
	if err := validateServeConfiguration(serveConfiguration{
		maxBodyBytes:              *maxBody,
		sessionSoftLimitBytes:     *sessionSoftLimit,
		sessionHardLimitBytes:     *sessionHardLimit,
		maxSessionStateBytes:      *maxSessionStateBytes,
		maxTransferBytes:          *maxTransferBytes,
		maxTransferEvents:         *maxTransferEvents,
		maxConcurrentTransfers:    *maxConcurrentTransfers,
		requestTimeout:            *requestTimeout,
		transferTimeout:           *transferTimeout,
		transferInactivityTimeout: *transferInactivityTimeout,
		scrubInterval:             *scrubInterval,
		scrubTimeout:              *scrubTimeout,
		scrubMaxEvents:            *scrubMaxEvents,
	}); err != nil {
		return err
	}
	token := os.Getenv("RIN_TOKEN")
	if err := validateListenAddress(*address, *allowRemote, token); err != nil {
		return err
	}
	fileStore, err := store.OpenFile(*dataDirectory)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, fileStore.Close())
	}()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	modelRuntime, err := buildModelRuntime(logger)
	if err != nil {
		return err
	}
	engine, err := rinruntime.OpenWithOptions(
		fileStore,
		modelRuntime.DecisionProvider,
		rinruntime.EngineOptions{
			SessionSoftLimitBytes:  *sessionSoftLimit,
			SessionHardLimitBytes:  *sessionHardLimit,
			MaxSessionStateBytes:   *maxSessionStateBytes,
			MaxTransferBytes:       *maxTransferBytes,
			MaxTransferEvents:      *maxTransferEvents,
			MaxConcurrentTransfers: *maxConcurrentTransfers,
		},
	)
	if err != nil {
		return err
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, engine.Close(closeContext))
	}()
	jobManager, err := jobs.New(engine, jobs.Config{
		Workers: envInt("RIN_JOB_WORKERS", 2), QueueSize: envInt("RIN_JOB_QUEUE_SIZE", 64),
		MaxJobs: envInt("RIN_JOB_MAX_RETAINED", 512), JobTTL: envDuration("RIN_JOB_TTL", 30*time.Minute),
	})
	if err != nil {
		return err
	}
	var generationManager *generation.Manager
	if modelRuntime.GenerationProvider != nil {
		generationManager, err = generation.New(modelRuntime.GenerationProvider, generation.Config{
			Workers: envInt("RIN_GENERATION_WORKERS", 2), QueueSize: envInt("RIN_GENERATION_QUEUE_SIZE", 64),
			MaxJobs: envInt("RIN_GENERATION_MAX_RETAINED", 512), JobTTL: envDuration("RIN_GENERATION_JOB_TTL", 30*time.Minute),
			CacheEntries: envInt("RIN_GENERATION_CACHE_ENTRIES", 256), CacheTTL: envDuration("RIN_GENERATION_CACHE_TTL", 30*time.Minute),
			MaxOutputBytes: envInt("RIN_GENERATION_MAX_OUTPUT_BYTES", 512*1024),
			MaxRetainedBytes: envUint64(
				"RIN_GENERATION_MAX_RETAINED_BYTES",
				64<<20,
			),
			CleanupInterval: envDuration(
				"RIN_GENERATION_CLEANUP_INTERVAL",
				time.Minute,
			),
		})
		if err != nil {
			closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = jobManager.Close(closeContext)
			return err
		}
	}
	api := httpapi.New(engine, httpapi.Options{
		Token: token, MaxBodyBytes: *maxBody, Logger: logger, Jobs: jobManager,
		Generation: generationManager, PolicyMode: modelRuntime.Mode,
		RequestTimeout: *requestTimeout, TransferTimeout: *transferTimeout,
		TransferInactivityTimeout: *transferInactivityTimeout,
	})
	server := httpapi.NewProductionServer(*address, api)
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = jobManager.Close(closeContext)
		if generationManager != nil {
			_ = generationManager.Close(closeContext)
		}
		return err
	}
	logFields := []any{
		"address", listener.Addr().String(),
		"protocol", protocol.Version,
		"auth", token != "",
		"policy", modelRuntime.Mode,
		"structured_generation", generationManager != nil,
		"session_soft_limit_bytes", *sessionSoftLimit,
		"session_hard_limit_bytes", *sessionHardLimit,
		"session_state_max_bytes", *maxSessionStateBytes,
		"transfer_max_bytes", *maxTransferBytes,
		"transfer_max_events", *maxTransferEvents,
		"transfer_max_concurrent", *maxConcurrentTransfers,
		"request_timeout", *requestTimeout,
		"transfer_timeout", *transferTimeout,
		"transfer_inactivity_timeout", *transferInactivityTimeout,
		"scrub_interval", *scrubInterval,
		"scrub_timeout", *scrubTimeout,
		"scrub_max_events", *scrubMaxEvents,
	}
	if modelRuntime.Mode == "model-with-fallback" {
		logFields = append(logFields, "model_config", describeModelConfig())
	}
	logger.Info("rin sidecar listening", logFields...)
	errChannel := make(chan error, 1)
	scrubContext, cancelScrub := context.WithCancel(context.Background())
	scrubDone := make(chan struct{})
	go func() {
		defer close(scrubDone)
		runScrubLoop(
			scrubContext,
			engine,
			logger,
			*scrubInterval,
			*scrubTimeout,
			*scrubMaxEvents,
		)
	}()
	go func() {
		errChannel <- server.Serve(listener)
	}()
	signalContext, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	var serveError error
	shutdownRequested := false
	select {
	case err := <-errChannel:
		if !errors.Is(err, http.ErrServerClosed) {
			serveError = err
		}
	case <-signalContext.Done():
		shutdownRequested = true
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var shutdownError error
	if shutdownRequested {
		shutdownError = server.Shutdown(shutdownContext)
	}
	cancelScrub()
	var scrubError error
	select {
	case <-scrubDone:
	case <-shutdownContext.Done():
		scrubError = shutdownContext.Err()
	}
	jobsError := jobManager.Close(shutdownContext)
	var generationError error
	if generationManager != nil {
		generationError = generationManager.Close(shutdownContext)
	}
	runtimeError := engine.Close(shutdownContext)
	return errors.Join(
		serveError,
		shutdownError,
		jobsError,
		generationError,
		scrubError,
		runtimeError,
	)
}

type runtimeScrubber interface {
	Scrub(context.Context, int) (rinruntime.ScrubReport, error)
}

func runScrubLoop(
	ctx context.Context,
	scrubber runtimeScrubber,
	logger *slog.Logger,
	interval time.Duration,
	timeout time.Duration,
	maxEvents int,
) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		passContext, cancel := context.WithTimeout(ctx, timeout)
		report, err := scrubber.Scrub(passContext, maxEvents)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Warn(
				"incremental event-log scrub did not complete",
				"error", err,
				"code", rinruntime.ErrorCode(err),
				"revision", report.Revision,
				"target_revision", report.TargetRevision,
			)
		} else if report.CycleComplete {
			logger.Info(
				"incremental event-log scrub cycle completed",
				"completed_cycles", report.CompletedCycles,
				"checked_events", report.CheckedEvents,
				"completed_sessions", report.CompletedSessions,
			)
		}
		timer.Reset(interval)
	}
}

func validateListenAddress(address string, allowRemote bool, token string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	loopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		loopback = ip.IsLoopback()
	}
	if !loopback && !allowRemote {
		return errors.New("non-loopback address requires -allow-remote")
	}
	if !loopback && token == "" {
		return errors.New("non-loopback address requires RIN_TOKEN")
	}
	return nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envUint64(key string, fallback uint64) uint64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}
