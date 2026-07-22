package compat_test

import (
	"os"
	"strings"
	"testing"
)

func TestEngineExamplesPreserveAsyncAuthorityBoundary(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		required  []string
		forbidden []string
	}{
		{
			name: "godot",
			path: "../examples/godot/rin_client.gd",
			required: []string{
				"await request.request_completed",
				"request.max_redirects = 0",
				"request.body_size_limit = max_response_bytes",
				"HTTPClient.METHOD_DELETE",
				"\"committable\": false",
				"\"policy_source\": \"adapter-offline\"",
				"/v1/session/activity",
				"/v1/world/arbitrate",
				"/v1/session/timeline",
			},
			forbidden: []string{"OS.execute", "FileAccess.open", "Thread.wait_to_finish"},
		},
		{
			name: "unity",
			path: "../examples/unity/RinClient.cs",
			required: []string{
				"UnityWebRequest",
				"request.redirectLimit = 0",
				"CappedDownloadHandler",
				"WaitForSecondsRealtime",
				"committable = false",
				"policy_source = \"adapter-offline\"",
				"/v1/session/activity",
				"/v1/world/arbitrate",
				"/v1/session/timeline",
			},
			forbidden: []string{"Thread.Sleep", ".Wait()", "Process.Start"},
		},
		{
			name: "renpy",
			path: "../adapters/renpy/rin_client.py",
			required: []string{
				"class _NoRedirectHandler",
				"class BackgroundProposalRegistry",
				"committable\": False",
				"adapter-offline",
				"/v1/session/activity",
				"/v1/world/arbitrate",
				"/v1/session/timeline",
			},
			forbidden: []string{"import requests", "subprocess", "os.system"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(payload)
			for _, required := range test.required {
				if !strings.Contains(text, required) {
					t.Fatalf("%s is missing %q", test.path, required)
				}
			}
			for _, forbidden := range test.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s contains forbidden pattern %q", test.path, forbidden)
				}
			}
		})
	}
}
