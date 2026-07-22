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
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rin:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if len(arguments) > 0 && arguments[0] == "inspect" {
		return runInspect(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && arguments[0] == "serve" {
		arguments = arguments[1:]
	}
	flags := flag.NewFlagSet("rin serve", flag.ContinueOnError)
	address := flags.String("addr", envOr("RIN_ADDR", "127.0.0.1:7374"), "listen address")
	dataDirectory := flags.String("data", envOr("RIN_DATA_DIR", "./rin-data"), "event and snapshot directory")
	allowRemote := flags.Bool("allow-remote", false, "allow a non-loopback listen address")
	maxBody := flags.Int64("max-body-bytes", envInt64("RIN_MAX_BODY_BYTES", httpapi.DefaultMaxBodyBytes), "maximum JSON request size")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	token := os.Getenv("RIN_TOKEN")
	if err := validateListenAddress(*address, *allowRemote, token); err != nil {
		return err
	}
	fileStore, err := store.OpenFile(*dataDirectory)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	modelRuntime, err := buildModelRuntime(logger)
	if err != nil {
		return err
	}
	engine, err := rinruntime.Open(fileStore, modelRuntime.Policy)
	if err != nil {
		return err
	}
	jobManager, err := jobs.New(engine, jobs.Config{
		Workers: envInt("RIN_JOB_WORKERS", 2), QueueSize: envInt("RIN_JOB_QUEUE_SIZE", 64),
		MaxJobs: envInt("RIN_JOB_MAX_RETAINED", 512), JobTTL: envDuration("RIN_JOB_TTL", 30*time.Minute),
	})
	if err != nil {
		return err
	}
	var generationManager *generation.Manager
	if modelRuntime.GenerationClient != nil {
		generationManager, err = generation.New(modelRuntime.GenerationClient, generation.Config{
			Workers: envInt("RIN_GENERATION_WORKERS", 2), QueueSize: envInt("RIN_GENERATION_QUEUE_SIZE", 64),
			MaxJobs: envInt("RIN_GENERATION_MAX_RETAINED", 512), JobTTL: envDuration("RIN_GENERATION_JOB_TTL", 30*time.Minute),
			CacheEntries: envInt("RIN_GENERATION_CACHE_ENTRIES", 256), CacheTTL: envDuration("RIN_GENERATION_CACHE_TTL", 30*time.Minute),
			MaxOutputBytes: envInt("RIN_GENERATION_MAX_OUTPUT_BYTES", 512*1024),
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
	})
	server := &http.Server{
		Addr:              *address,
		Handler:           api,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
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
	logFields := []any{"address", listener.Addr().String(), "protocol", "rin.protocol/v1", "auth", token != "", "policy", modelRuntime.Mode, "structured_generation", generationManager != nil}
	if modelRuntime.Mode == "model-with-fallback" {
		logFields = append(logFields, "model_config", describeModelConfig())
	}
	logger.Info("rin sidecar listening", logFields...)
	errChannel := make(chan error, 1)
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
	jobsError := jobManager.Close(shutdownContext)
	var generationError error
	if generationManager != nil {
		generationError = generationManager.Close(shutdownContext)
	}
	return errors.Join(serveError, shutdownError, jobsError, generationError)
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
