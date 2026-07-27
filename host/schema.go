package host

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/sunrioa/rin/internal/jsonwire"
)

const (
	maxSchemaBytes   = 64 << 10
	maxInstanceBytes = 1 << 20
)

type denySchemaLoader struct{}

func (denySchemaLoader) Load(location string) (any, error) {
	return nil, fmt.Errorf("external schema resource %q is not allowed", location)
}

// CanonicalizeSchema validates a self-contained object schema, removes caller
// formatting, and sorts object keys through encoding/json. The returned bytes
// are the only bytes used for Schema and descriptor digests.
func CanonicalizeSchema(document []byte) ([]byte, error) {
	canonical, _, err := prepareSchema(document)
	return canonical, err
}

func prepareSchema(document []byte) ([]byte, *jsonschema.Schema, error) {
	if len(document) == 0 || len(document) > maxSchemaBytes {
		return nil, nil, invalid("schema.document", "must contain 1-65536 bytes")
	}
	if err := jsonwire.Validate(document); err != nil {
		return nil, nil, invalid("schema.document", err.Error())
	}

	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, invalid("schema.document", "must be valid JSON: "+err.Error())
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, nil, invalid("schema.document", err.Error())
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, nil, invalid("schema.document", "root must be an object")
	}
	if root["$schema"] != SchemaDialect {
		return nil, nil, invalid("schema.document.$schema", "must equal "+SchemaDialect)
	}
	if root["type"] != "object" {
		return nil, nil, invalid("schema.document.type", "must equal object")
	}
	if additional, exists := root["additionalProperties"]; !exists || additional != false {
		return nil, nil, invalid(
			"schema.document.additionalProperties",
			"must be present and equal false",
		)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, nil, invalid("schema.document", "cannot canonicalize: "+err.Error())
	}
	compiled, err := compileSchema(canonical)
	if err != nil {
		return nil, nil, invalid("schema.document", "does not compile: "+err.Error())
	}
	return canonical, compiled, nil
}

// NewSchema validates and canonicalizes a self-contained JSON Schema document.
func NewSchema(document []byte) (Schema, error) {
	canonical, _, err := prepareSchema(document)
	if err != nil {
		return Schema{}, err
	}
	return Schema{
		Dialect:  SchemaDialect,
		Document: canonical,
		SHA256:   sha256Hex(canonical),
	}, nil
}

// Validate verifies the schema dialect, canonical document, and digest.
func (schema Schema) Validate() error {
	_, err := schema.compiled()
	return err
}

// ValidateInstance validates one bounded JSON object against the schema.
func (schema Schema) ValidateInstance(document []byte) error {
	compiled, err := schema.compiled()
	if err != nil {
		return err
	}
	if len(document) > maxInstanceBytes {
		return invalid("instance", "must contain at most 1048576 bytes")
	}
	instance, err := decodeJSON(document)
	if err != nil {
		return invalid("instance", err.Error())
	}
	if err := compiled.Validate(instance); err != nil {
		return invalid("instance", err.Error())
	}
	return nil
}

func (schema Schema) compiled() (*jsonschema.Schema, error) {
	if schema.Dialect != SchemaDialect {
		return nil, invalid("schema.dialect", "must equal "+SchemaDialect)
	}
	canonical, compiled, err := prepareSchema(schema.Document)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(schema.Document, canonical) {
		return nil, invalid("schema.document", "must be canonical; construct it with NewSchema")
	}
	if schema.SHA256 != sha256Hex(canonical) {
		return nil, invalid("schema.sha256", "does not match canonical document")
	}
	return compiled, nil
}

func compileSchema(canonical []byte) (*jsonschema.Schema, error) {
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(canonical))
	if err != nil {
		return nil, err
	}
	location := "urn:rin:host-schema:" + sha256Hex(canonical)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denySchemaLoader{})
	if err := compiler.AddResource(location, value); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func decodeJSON(document []byte) (any, error) {
	if len(document) == 0 {
		return nil, errors.New("is required")
	}
	if err := jsonwire.Validate(document); err != nil {
		return nil, err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		return nil, fmt.Errorf("must be valid JSON: %w", err)
	}
	return value, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("must contain exactly one JSON value")
	}
	return fmt.Errorf("has trailing data: %w", err)
}

func sha256Hex(document []byte) string {
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}
