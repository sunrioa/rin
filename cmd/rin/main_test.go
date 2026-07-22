package main

import "testing"

func TestValidateListenAddress(t *testing.T) {
	tests := []struct {
		name        string
		address     string
		allowRemote bool
		token       string
		wantError   bool
	}{
		{name: "IPv4 loopback", address: "127.0.0.1:7374"},
		{name: "IPv6 loopback", address: "[::1]:7374"},
		{name: "localhost", address: "localhost:7374"},
		{name: "remote denied", address: "0.0.0.0:7374", wantError: true},
		{name: "remote needs token", address: "0.0.0.0:7374", allowRemote: true, wantError: true},
		{name: "remote explicit", address: "0.0.0.0:7374", allowRemote: true, token: "token"},
		{name: "invalid", address: "7374", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateListenAddress(test.address, test.allowRemote, test.token)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v wantError=%v", err, test.wantError)
			}
		})
	}
}
