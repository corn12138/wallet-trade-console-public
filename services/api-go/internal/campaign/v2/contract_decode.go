package v2

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"
)

//go:embed contract.generated.json
var contractSchema []byte

type schemaNode struct {
	Ref        string                `json:"$ref"`
	Type       string                `json:"type"`
	Properties map[string]schemaNode `json:"properties"`
	Required   []string              `json:"required"`
	Const      json.RawMessage       `json:"const"`
	Enum       []string              `json:"enum"`
	MinLength  int                   `json:"minLength"`
	MaxLength  *int                  `json:"maxLength"`
	Minimum    int64                 `json:"minimum"`
	Maximum    int64                 `json:"maximum"`
	Pattern    string                `json:"pattern"`
	AnyOf      []schemaNode          `json:"anyOf"`
	Items      *schemaNode           `json:"items"`
	MinItems   int                   `json:"minItems"`
	MaxItems   int                   `json:"maxItems"`
}

// contractSet is one schema's definitions; references resolve within it.
type contractSet map[string]schemaNode

var revisionContract = mustContractSet(contractSchema)

func mustContractSet(raw []byte) contractSet {
	var schema struct {
		Defs map[string]schemaNode `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		panic(err)
	}
	return schema.Defs
}

// DecodeRevision retains the original API while sharing the same strict JSON
// handling with HTTP command and query contracts.
func DecodeRevision(raw []byte) (RevisionInput, error) {
	var result RevisionInput
	err := DecodeContract("RevisionInput", raw, &result)
	return result, err
}

// DecodeContract checks a named, generated closed contract before populating a
// target. Wire shape does not authorize an operation or enforce its domain CAS.
func DecodeContract(name string, raw []byte, target any) error {
	return decodeContract(name, raw, target, 64*1024)
}

// DecodeContractResponse permits bounded list/history responses up to 1 MiB;
// request adapters must keep using DecodeContract's 64 KiB boundary.
func DecodeContractResponse(name string, raw []byte, target any) error {
	return decodeContract(name, raw, target, 1024*1024)
}

func decodeContract(name string, raw []byte, target any, limit int) error {
	return revisionContract.decode(name, raw, target, limit)
}

// DecodeContractResponseUnder decodes a response with another version of the
// campaign schema. Compatibility tests use it to prove what a client built
// against that version accepts.
func DecodeContractResponseUnder(schema []byte, name string, raw []byte, target any) error {
	var parsed struct {
		Defs contractSet `json:"$defs"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil || len(parsed.Defs) == 0 {
		return InvalidInput("campaign schema")
	}
	return parsed.Defs.decode(name, raw, target, 1024*1024)
}

func (set contractSet) decode(name string, raw []byte, target any, limit int) error {
	contract, ok := set[name]
	if !ok {
		return InvalidInput("unknown campaign contract")
	}
	if len(raw) > limit || !utf8.Valid(raw) || validateUnicodeEscapes(raw) != nil {
		return InvalidInput("contract encoding or size")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeUniqueJSON(decoder, 0)
	if err != nil {
		return InvalidInput("invalid JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return InvalidInput("multiple JSON values")
	}
	normalized, err := set.validateWire(value, contract)
	if err != nil {
		var domain *DomainError
		if errors.As(err, &domain) {
			return domain
		}
		return InvalidInput(err.Error())
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	result := json.NewDecoder(bytes.NewReader(encoded))
	result.DisallowUnknownFields()
	if err = result.Decode(target); err != nil {
		return InvalidInput("invalid contract target")
	}
	return nil
}

// This interpreter accepts the schema's deliberately small vocabulary only;
// the generator and schema tests reject unsupported additions before shipping.
func (set contractSet) validateWire(value any, schema schemaNode) (any, error) {
	if len(schema.AnyOf) > 0 {
		if len(schema.AnyOf) != 2 || schema.AnyOf[1].Type != "null" {
			return nil, errors.New("unsupported nullable contract")
		}
		if value == nil {
			return nil, nil
		}
		return set.validateWire(value, schema.AnyOf[0])
	}
	if schema.Ref != "" {
		if !strings.HasPrefix(schema.Ref, "#/$defs/") {
			return nil, errors.New("unsupported contract reference")
		}
		name := schema.Ref[len("#/$defs/"):]
		resolved, ok := set[name]
		if !ok {
			return nil, errors.New("unknown contract reference")
		}
		return set.validateWire(value, resolved)
	}
	switch schema.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("expected object")
		}
		for _, key := range schema.Required {
			if _, ok := object[key]; !ok {
				return nil, fmt.Errorf("missing %s", key)
			}
		}
		for key, raw := range object {
			field, ok := schema.Properties[key]
			if !ok {
				return nil, fmt.Errorf("unsupported field %s", key)
			}
			parsed, err := set.validateWire(raw, field)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			object[key] = parsed
		}
	case "array":
		values, ok := value.([]any)
		if !ok || schema.Items == nil || len(values) < schema.MinItems || len(values) > schema.MaxItems || schema.MaxItems > 50 {
			return nil, errors.New("array outside bounds")
		}
		for index, item := range values {
			parsed, err := set.validateWire(item, *schema.Items)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			values[index] = parsed
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return nil, errors.New("expected boolean")
		}
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return nil, errors.New("expected integer")
		}
		if !boundedDecimal(string(number)) {
			return nil, errors.New("numeric representation too large")
		}
		// Exact decimal parsing prevents 1.00000000000000001 from rounding
		// into a supported version before the const check.
		parsed, ok := new(big.Rat).SetString(string(number))
		if !ok || !parsed.IsInt() || !parsed.Num().IsInt64() || parsed.Num().Cmp(big.NewInt(1<<53-1)) > 0 || parsed.Num().Cmp(big.NewInt(-(1<<53-1))) < 0 {
			return nil, errors.New("invalid integer")
		}
		value = parsed.Num().Int64()
		if len(schema.Const) == 0 && (value.(int64) < schema.Minimum || value.(int64) > schema.Maximum) {
			return nil, errors.New("integer outside range")
		}
	case "string":
		text, ok := value.(string)
		if !ok || strings.ContainsRune(text, 0) {
			return nil, errors.New("expected JSONB-compatible string")
		}
		length := utf8.RuneCountInString(text)
		if length < schema.MinLength || (schema.MaxLength != nil && length > *schema.MaxLength) {
			return nil, errors.New("string outside length range")
		}
		if schema.Pattern != "" {
			matched, err := regexp.MatchString(schema.Pattern, text)
			if err != nil || !matched {
				return nil, errors.New("invalid string format")
			}
		}
		if len(schema.Enum) > 0 {
			found := false
			for _, candidate := range schema.Enum {
				found = found || candidate == text
			}
			if !found {
				return nil, &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "unsupported enum"}
			}
		}
	default:
		return nil, errors.New("unsupported contract type")
	}
	if len(schema.Const) > 0 {
		actual, _ := json.Marshal(value)
		if !bytes.Equal(actual, schema.Const) {
			return nil, &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "unsupported contract version or rule"}
		}
	}
	return value, nil
}
