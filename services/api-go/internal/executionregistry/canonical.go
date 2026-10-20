package executionregistry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
)

// Canonical is the §3 canonical JSON: keys sorted bytewise, no insignificant
// whitespace, no HTML escaping. Callers pass integers as decimal strings, so
// no float formatting is involved. The Web implements the same function and
// the shared corpus pins both.
func Canonical(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalize(value)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// normalize turns json.Number back into its literal so a re-encoded document
// keeps the source digits.
func normalize(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = normalize(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = normalize(item)
		}
		return out
	default:
		return v
	}
}

// Digest is sha256(canonical(value)) as lowercase hex.
func Digest(value any) (string, error) {
	raw, err := Canonical(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func mustDigest(value any) string {
	d, err := Digest(value)
	if err != nil {
		panic(err)
	}
	return d
}

// decodeStrict refuses unknown fields and trailing data.
func decodeStrict(raw []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

func decimalWord(word []byte) string {
	return new(big.Int).SetBytes(word).String()
}
