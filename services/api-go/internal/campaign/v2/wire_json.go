package v2

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

func boundedDecimal(value string) bool {
	if len(value) > 128 {
		return false
	}
	if at := strings.IndexAny(value, "eE"); at >= 0 {
		exponent, err := strconv.ParseInt(value[at+1:], 10, 16)
		return err == nil && exponent >= -128 && exponent <= 128
	}
	return true
}

// Duplicate keys and unpaired UTF-16 escapes are rejected before decoding can
// erase them. Otherwise two implementations can hash different accepted terms.
func validateUnicodeEscapes(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return errors.New("invalid unicode escape")
		}
		unit, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return err
		}
		i += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return errors.New("unpaired unicode surrogate")
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			if i+6 >= len(raw) || string(raw[i+1:i+3]) != `\u` {
				return errors.New("unpaired unicode surrogate")
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("unpaired unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}

func decodeUniqueJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth > 24 {
		return nil, errors.New("contract nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		if delimiter == '[' {
			values := make([]any, 0)
			for decoder.More() {
				if len(values) >= 50 {
					return nil, errors.New("array exceeds transport bound")
				}
				value, err := decodeUniqueJSON(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated array")
			}
			return values, nil
		}
		if delimiter != '{' {
			return nil, errors.New("unsupported JSON container")
		}
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate object key")
			}
			value, err := decodeUniqueJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, errors.New("unterminated object")
		}
		return object, nil
	}
	return token, nil
}
