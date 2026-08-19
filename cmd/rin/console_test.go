package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConsoleChecksServiceBeforeOpeningConsole(t *testing.T) {
	var paths []string
	var output bytes.Buffer
	opened := ""
	err := runConsoleWithOpenerAndClient(
		[]string{"--url", "http://127.0.0.1:7375", "--open=true"},
		&output,
		io.Discard,
		func(string) (string, bool) { return "", false },
		func(target string) error {
			opened = target
			return nil
		},
		&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			paths = append(paths, request.URL.Path)
			return &http.Response{
				StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
			}, nil
		})},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/control/v2/health" {
		t.Fatalf("health paths = %v", paths)
	}
	if opened != "http://127.0.0.1:7375/console/" {
		t.Fatalf("opened URL = %q", opened)
	}
	if !strings.Contains(output.String(), "http://127.0.0.1:7375/console/") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestConsoleDoesNotOpenWhenDisabled(t *testing.T) {
	opened := false
	err := runConsoleWithOpenerAndClient(
		[]string{"--url", "http://127.0.0.1:7375", "--open=false"},
		io.Discard,
		io.Discard,
		func(string) (string, bool) { return "", false },
		func(string) error {
			opened = true
			return nil
		},
		&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNoContent, Status: "204 No Content", Body: io.NopCloser(strings.NewReader("")),
			}, nil
		})},
	)
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("console was opened with --open=false")
	}
}

func TestConsoleRejectsNonLoopbackURL(t *testing.T) {
	if _, err := parseConsoleURL("http://example.com:7375"); err == nil {
		t.Fatal("non-loopback URL was accepted")
	}
	if _, err := parseConsoleURL("http://127.0.0.1:0"); err == nil {
		t.Fatal("zero port URL was accepted")
	}
}

func TestConsoleHelpReturnsSuccess(t *testing.T) {
	if err := runConsole([]string{"--help"}, io.Discard, io.Discard, func(string) (string, bool) {
		return "", false
	}); err != nil {
		t.Fatalf("runConsole(--help): %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
