package jsonwire

import "testing"

func TestValid(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{name: "object", payload: []byte(`{"text":"hello"}`), want: true},
		{name: "literal unicode", payload: []byte(`{"text":"你好 😀"}`), want: true},
		{name: "replacement character is valid data", payload: []byte(`{"text":"\ufffd"}`), want: true},
		{name: "surrogate pair", payload: []byte(`{"text":"\ud83d\ude00"}`), want: true},
		{name: "escaped backslash is ordinary text", payload: []byte(`{"text":"\\ud800"}`), want: true},
		{name: "invalid raw UTF-8", payload: []byte{'"', 0xff, '"'}, want: false},
		{name: "unpaired high surrogate", payload: []byte(`{"text":"\ud800"}`), want: false},
		{name: "unpaired low surrogate", payload: []byte(`{"text":"\udc00"}`), want: false},
		{name: "two high surrogates", payload: []byte(`{"text":"\ud800\ud801"}`), want: false},
		{name: "high surrogate followed by text", payload: []byte(`{"text":"\ud800x"}`), want: false},
		{name: "invalid JSON", payload: []byte(`{"text":`), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Valid(test.payload); got != test.want {
				t.Fatalf("Valid(%q) = %v, want %v", test.payload, got, test.want)
			}
		})
	}
}
