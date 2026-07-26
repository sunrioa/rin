package host

import (
	"bytes"
	"strings"
	"testing"
)

func TestSchemaCanonicalizationAndValidation(t *testing.T) {
	first, err := NewSchema([]byte(`{
		"type": "object",
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"required": ["target"],
		"properties": {"target": {"type": "string", "minLength": 1}},
		"additionalProperties": false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSchema([]byte(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
			`"additionalProperties":false,"properties":{"target":{"minLength":1,"type":"string"}},` +
			`"required":["target"],"type":"object"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Document, second.Document) || first.SHA256 != second.SHA256 {
		t.Fatalf("equivalent schemas did not canonicalize equally:\n%s\n%s",
			first.Document, second.Document)
	}
	if err := first.ValidateInstance([]byte(`{"target":"dock"}`)); err != nil {
		t.Fatalf("valid instance rejected: %v", err)
	}
	for _, document := range []string{
		`{"target":""}`,
		`{"target":"dock","hidden":true}`,
		`{"target":"dock","target":"harbor"}`,
		`[]`,
	} {
		if err := first.ValidateInstance([]byte(document)); err == nil {
			t.Fatalf("invalid instance accepted: %s", document)
		}
	}

	tampered := first
	tampered.Document = bytes.Replace(
		first.Document,
		[]byte(`"minLength":1`),
		[]byte(`"minLength":2`),
		1,
	)
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered schema digest accepted")
	}
}

func TestSchemaRejectsAmbiguousOrRemoteDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
	}{
		{
			name: "duplicate",
			document: `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
				`"type":"object","type":"object","additionalProperties":false}`,
			want: "duplicate object name",
		},
		{
			name: "open object",
			document: `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
				`"type":"object"}`,
			want: "additionalProperties",
		},
		{
			name: "wrong dialect",
			document: `{"$schema":"http://json-schema.org/draft-07/schema#",` +
				`"type":"object","additionalProperties":false}`,
			want: "$schema",
		},
		{
			name: "remote ref",
			document: `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
				`"type":"object","properties":{"x":{"$ref":"https://example.com/x.json"}},` +
				`"additionalProperties":false}`,
			want: "external schema resource",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSchema([]byte(test.document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func FuzzCanonicalizeSchemaDoesNotPanic(f *testing.F) {
	f.Add([]byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false}`))
	f.Add([]byte(`{"x":1,"x":2}`))
	f.Add([]byte{0xff, 0x00, '{'})
	f.Fuzz(func(t *testing.T, document []byte) {
		_, _ = CanonicalizeSchema(document)
	})
}
