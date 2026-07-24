package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const localSchemaPrefix = "#/components/schemas/"

type ShapeErrorKind string

const (
	ShapeErrorRequired ShapeErrorKind = "required"
	ShapeErrorUnknown  ShapeErrorKind = "unknown"
)

// ShapeError reports a request-shape violation using the same dotted field
// notation as protocol.ValidationError.
type ShapeError struct {
	Kind    ShapeErrorKind
	Field   string
	Message string
}

func (e *ShapeError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

type requestShapeContract struct {
	schemas map[string]map[string]any
}

var (
	requestShapeOnce     sync.Once
	parsedRequestShape   requestShapeContract
	requestShapeParseErr error
)

// ValidateRequestShape checks required-member presence and recursively rejects
// unknown members only for OpenAPI objects with additionalProperties=false.
// Value constraints remain the responsibility of typed protocol validators.
func ValidateRequestShape(
	schemaName string,
	payload []byte,
) (*ShapeError, error) {
	contract, err := loadRequestShapeContract()
	if err != nil {
		return nil, err
	}
	schema, exists := contract.schemas[schemaName]
	if !exists {
		return nil, fmt.Errorf("OpenAPI component schema %q does not exist", schemaName)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil
	}
	return contract.validate(schema, value, "", 0)
}

// ValidateRequiredFields is kept as a narrow compatibility name for callers
// that adopted the initial contract API. It now performs full request-shape
// validation, including closed-object unknown-member checks.
func ValidateRequiredFields(
	schemaName string,
	payload []byte,
) (*ShapeError, error) {
	return ValidateRequestShape(schemaName, payload)
}

func loadRequestShapeContract() (requestShapeContract, error) {
	requestShapeOnce.Do(func() {
		var document struct {
			Components struct {
				Schemas map[string]map[string]any `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(openAPIDocument, &document); err != nil {
			requestShapeParseErr = fmt.Errorf("decode embedded OpenAPI schemas: %w", err)
			return
		}
		if len(document.Components.Schemas) == 0 {
			requestShapeParseErr = fmt.Errorf("embedded OpenAPI contract has no component schemas")
			return
		}
		parsedRequestShape = requestShapeContract{schemas: document.Components.Schemas}
	})
	return parsedRequestShape, requestShapeParseErr
}

func (c requestShapeContract) validate(
	schema map[string]any,
	value any,
	field string,
	depth int,
) (*ShapeError, error) {
	if depth > 128 {
		return nil, fmt.Errorf("OpenAPI schema nesting exceeds the supported depth")
	}
	if value == nil && !schemaAllowsNull(schema) {
		return &ShapeError{
			Kind: ShapeErrorRequired, Field: field, Message: "must not be null",
		}, nil
	}
	if reference, ok := schema["$ref"].(string); ok {
		referenced, err := c.resolve(reference)
		if err != nil {
			return nil, err
		}
		if requiredErr, err := c.validate(referenced, value, field, depth+1); requiredErr != nil || err != nil {
			return requiredErr, err
		}
	}
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, member := range allOf {
			memberSchema, memberOK := member.(map[string]any)
			if !memberOK {
				return nil, fmt.Errorf("OpenAPI allOf member is not a schema object")
			}
			if requiredErr, err := c.validate(memberSchema, value, field, depth+1); requiredErr != nil || err != nil {
				return requiredErr, err
			}
		}
	}
	if value == nil {
		return nil, nil
	}

	if object, ok := value.(map[string]any); ok {
		properties, err := schemaProperties(schema)
		if err != nil {
			return nil, err
		}
		closed := schema["additionalProperties"] == false
		if closed {
			unknownNames := make([]string, 0)
			for name := range object {
				if _, declared := properties[name]; !declared {
					unknownNames = append(unknownNames, name)
				}
			}
			sort.Strings(unknownNames)
			if len(unknownNames) > 0 {
				return &ShapeError{
					Kind:    ShapeErrorUnknown,
					Field:   joinField(field, unknownNames[0]),
					Message: "is not allowed by the request schema",
				}, nil
			}
		}

		// Additive response/state objects may carry unknown future members, but
		// every member known to this contract still obeys its required presence.
		requiredNames, err := schemaRequiredNames(schema)
		if err != nil {
			return nil, err
		}
		for _, name := range requiredNames {
			member, exists := object[name]
			memberField := joinField(field, name)
			if !exists {
				return &ShapeError{
					Kind: ShapeErrorRequired, Field: memberField, Message: "is required",
				}, nil
			}
			if member == nil {
				propertySchema, _ := schemaProperty(schema, name)
				if !schemaAllowsNull(propertySchema) {
					return &ShapeError{
						Kind: ShapeErrorRequired, Field: memberField, Message: "must not be null",
					}, nil
				}
			}
		}

		propertyNames := make([]string, 0, len(properties))
		for name := range properties {
			propertyNames = append(propertyNames, name)
		}
		sort.Strings(propertyNames)
		for _, name := range propertyNames {
			propertySchema := properties[name]
			member, exists := object[name]
			if !exists {
				continue
			}
			if requiredErr, err := c.validate(
				propertySchema,
				member,
				joinField(field, name),
				depth+1,
			); requiredErr != nil || err != nil {
				return requiredErr, err
			}
		}
		if additional, ok := schema["additionalProperties"].(map[string]any); ok {
			additionalNames := make([]string, 0, len(object))
			for name := range object {
				if _, declared := properties[name]; !declared {
					additionalNames = append(additionalNames, name)
				}
			}
			sort.Strings(additionalNames)
			for _, name := range additionalNames {
				member := object[name]
				if requiredErr, err := c.validate(
					additional,
					member,
					joinField(field, name),
					depth+1,
				); requiredErr != nil || err != nil {
					return requiredErr, err
				}
			}
		}
	}

	if array, ok := value.([]any); ok {
		itemSchema, ok := schema["items"].(map[string]any)
		if !ok {
			return nil, nil
		}
		for index, member := range array {
			itemField := field + "[" + strconv.Itoa(index) + "]"
			if requiredErr, err := c.validate(
				itemSchema,
				member,
				itemField,
				depth+1,
			); requiredErr != nil || err != nil {
				return requiredErr, err
			}
		}
	}
	return nil, nil
}

func (c requestShapeContract) resolve(reference string) (map[string]any, error) {
	if !strings.HasPrefix(reference, localSchemaPrefix) {
		return nil, fmt.Errorf("unsupported non-local OpenAPI schema reference %q", reference)
	}
	name := strings.TrimPrefix(reference, localSchemaPrefix)
	name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
	schema, exists := c.schemas[name]
	if !exists {
		return nil, fmt.Errorf("OpenAPI schema reference %q does not exist", reference)
	}
	return schema, nil
}

func schemaRequiredNames(schema map[string]any) ([]string, error) {
	raw, exists := schema["required"]
	if !exists {
		return nil, nil
	}
	members, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("OpenAPI required keyword is not an array")
	}
	names := make([]string, 0, len(members))
	for _, member := range members {
		name, ok := member.(string)
		if !ok {
			return nil, fmt.Errorf("OpenAPI required member is not a string")
		}
		names = append(names, name)
	}
	return names, nil
}

func schemaProperties(schema map[string]any) (map[string]map[string]any, error) {
	raw, exists := schema["properties"]
	if !exists {
		return nil, nil
	}
	members, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("OpenAPI properties keyword is not an object")
	}
	properties := make(map[string]map[string]any, len(members))
	for name, member := range members {
		propertySchema, ok := member.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("OpenAPI property %q is not a schema object", name)
		}
		properties[name] = propertySchema
	}
	return properties, nil
}

func schemaProperty(schema map[string]any, name string) (map[string]any, bool) {
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	property, ok := properties[name].(map[string]any)
	return property, ok
}

func schemaAllowsNull(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	switch schemaType := schema["type"].(type) {
	case string:
		return schemaType == "null"
	case []any:
		for _, member := range schemaType {
			if member == "null" {
				return true
			}
		}
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		members, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		for _, member := range members {
			memberSchema, ok := member.(map[string]any)
			if ok && schemaAllowsNull(memberSchema) {
				return true
			}
		}
	}
	return false
}

func joinField(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}
