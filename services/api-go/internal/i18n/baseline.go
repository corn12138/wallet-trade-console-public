// Package i18n is the server-owned translation system (2026-07-10).
//
// PostgreSQL published catalog revisions are the runtime source of truth;
// this package owns catalog reads, draft writes, validation, publication,
// rollback, auditing, checksums and the enabled-locale metadata. The files
// under baseline/ are the bootstrap / degraded-mode content moved out of
// apps/web/messages — they seed the first drafts and serve requests only
// when the database holds no published revision (source "embedded-baseline").
// They are not a second editable source.
package i18n

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed baseline/*.json
var baselineFS embed.FS

// BaselineLocale describes one embedded bootstrap locale.
type BaselineLocale struct {
	Code        string
	EnglishName string
	NativeName  string
	IsDefault   bool
}

// BaselineLocales lists the embedded bootstrap locales. English is the
// bootstrap default (locale-resolution rule 4 in the task contract).
func BaselineLocales() []BaselineLocale {
	return []BaselineLocale{
		{Code: "en", EnglishName: "English", NativeName: "English", IsDefault: true},
		{Code: "zh", EnglishName: "Chinese (Simplified)", NativeName: "简体中文", IsDefault: false},
	}
}

// BaselineCatalog returns the embedded nested catalog for a locale, or an
// error when the locale has no embedded baseline.
func BaselineCatalog(locale string) (map[string]any, error) {
	raw, err := baselineFS.ReadFile("baseline/" + locale + ".json")
	if err != nil {
		return nil, fmt.Errorf("i18n: no embedded baseline for locale %q", locale)
	}
	var nested map[string]any
	if err := json.Unmarshal(raw, &nested); err != nil {
		return nil, fmt.Errorf("i18n: baseline %s.json is not valid JSON: %w", locale, err)
	}
	return nested, nil
}

// Flatten converts a nested catalog into (namespace, key, value) triples.
// The namespace is the top-level object key; the message key is the dotted
// path below it. Returns an error on non-string leaves or on a top-level
// leaf (every message must live inside a namespace).
type FlatMessage struct {
	Namespace string
	Key       string
	Value     string
}

func Flatten(nested map[string]any) ([]FlatMessage, error) {
	out := make([]FlatMessage, 0, 1024)
	for ns, sub := range nested {
		subObj, ok := sub.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("i18n: top-level key %q must be a namespace object", ns)
		}
		if err := flattenInto(&out, ns, "", subObj); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

func flattenInto(out *[]FlatMessage, ns, prefix string, obj map[string]any) error {
	for k, v := range obj {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch t := v.(type) {
		case map[string]any:
			if err := flattenInto(out, ns, path, t); err != nil {
				return err
			}
		case string:
			*out = append(*out, FlatMessage{Namespace: ns, Key: path, Value: t})
		default:
			return fmt.Errorf("i18n: %s.%s must be a string leaf, got %T", ns, path, v)
		}
	}
	return nil
}

// Nest rebuilds the nested catalog object from flat messages. Conflicting
// paths (a key that is both a leaf and a parent) return an error — the same
// condition validation rejects before publish.
func Nest(flat []FlatMessage) (map[string]any, error) {
	root := map[string]any{}
	for _, m := range flat {
		nsObj, ok := root[m.Namespace].(map[string]any)
		if !ok {
			if _, exists := root[m.Namespace]; exists {
				return nil, fmt.Errorf("i18n: namespace %q conflicts with a leaf", m.Namespace)
			}
			nsObj = map[string]any{}
			root[m.Namespace] = nsObj
		}
		parts := strings.Split(m.Key, ".")
		cur := nsObj
		for i, p := range parts {
			if i == len(parts)-1 {
				if _, exists := cur[p]; exists {
					return nil, fmt.Errorf("i18n: duplicate or conflicting key %s.%s", m.Namespace, m.Key)
				}
				cur[p] = m.Value
				continue
			}
			next, ok := cur[p].(map[string]any)
			if !ok {
				if _, exists := cur[p]; exists {
					return nil, fmt.Errorf("i18n: key %s.%s conflicts with a leaf ancestor", m.Namespace, m.Key)
				}
				next = map[string]any{}
				cur[p] = next
			}
			cur = next
		}
	}
	return root, nil
}

// CanonicalJSON marshals a catalog deterministically (encoding/json sorts
// map keys) with no insignificant whitespace, for stable checksums.
func CanonicalJSON(catalog map[string]any) ([]byte, error) {
	return json.Marshal(catalog)
}

// Checksum returns the lowercase hex SHA-256 of the canonical JSON bytes.
func Checksum(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
