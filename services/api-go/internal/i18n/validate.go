package i18n

import (
	"fmt"
	"regexp"
	"strings"
)

// Validation limits. Values are conservative product bounds, not tunables a
// request can raise.
const (
	MaxValueLen     = 4096
	MaxKeyLen       = 240
	MaxNamespaceLen = 120
)

var (
	namespaceRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,120}$`)
	keyRE       = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,240}$`)
	localeRE    = regexp.MustCompile(`^[a-z]{2,3}(-[a-zA-Z0-9]{2,8}){0,3}$`)
	// Plain text / ICU only. Values render as React text nodes, so the rule
	// targets real markup: closing tags, attribute-carrying tags, and the
	// dangerous element/URL/handler set. Bare placeholder tokens such as
	// "/profile/<address>" stay legal copy.
	htmlishRE = regexp.MustCompile(`(?i)(</\s*[a-z]|<\s*(script|iframe|img|svg|object|embed|style|link|meta|a)\b|<[a-z][^>]*\s[a-z-]+\s*=|javascript:|\bon[a-z]+\s*=)`)
)

// richTextAllowlist names the only keys allowed to carry sanitized markup.
// Empty by default — plain text/ICU is the product rule.
var richTextAllowlist = map[string]bool{}

// NormalizeLocale canonicalizes a BCP-47-ish code (lowercase language,
// as-given subtags lowercased) and reports whether it is structurally valid.
func NormalizeLocale(code string) (string, bool) {
	c := strings.ToLower(strings.TrimSpace(code))
	if !localeRE.MatchString(c) {
		return "", false
	}
	return c, true
}

// ValidKeyParts reports whether namespace and key are canonical.
func ValidKeyParts(namespace, key string) error {
	if !namespaceRE.MatchString(namespace) {
		return fmt.Errorf("invalid namespace %q", namespace)
	}
	if !keyRE.MatchString(key) || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") || strings.Contains(key, "..") {
		return fmt.Errorf("invalid message key %q", key)
	}
	return nil
}

// Issue is one validation finding. Fatal issues block publication.
type Issue struct {
	Namespace string `json:"namespace,omitempty"`
	Key       string `json:"key,omitempty"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
}

// ValidateValue checks a single draft value in isolation (used at draft-save
// time for fast feedback): length, plain-text rule and ICU syntax.
func ValidateValue(namespace, key, value string) []Issue {
	var issues []Issue
	full := namespace + "." + key
	if strings.TrimSpace(value) == "" {
		issues = append(issues, Issue{Namespace: namespace, Key: key, Code: "empty-value",
			Detail: "value must not be empty"})
		return issues
	}
	if len(value) > MaxValueLen {
		issues = append(issues, Issue{Namespace: namespace, Key: key, Code: "value-too-long",
			Detail: fmt.Sprintf("value exceeds %d bytes", MaxValueLen)})
	}
	if htmlishRE.MatchString(value) && !richTextAllowlist[full] {
		issues = append(issues, Issue{Namespace: namespace, Key: key, Code: "html-not-allowed",
			Detail: "markup is rejected; messages are plain text/ICU only"})
	}
	if _, err := ParseICU(value); err != nil {
		issues = append(issues, Issue{Namespace: namespace, Key: key, Code: "icu-syntax",
			Detail: err.Error()})
	}
	return issues
}

// ValidateCatalog validates a target locale's flat messages against the
// default locale's flat messages. It reports, per the publication contract:
// missing default keys, unexpected empty values, ICU syntax errors, ICU
// argument mismatches vs the default message, key-shape violations and
// leaf/parent conflicts (via Nest).
func ValidateCatalog(target, def []FlatMessage) []Issue {
	var issues []Issue

	tByKey := make(map[string]FlatMessage, len(target))
	for _, m := range target {
		fk := m.Namespace + "\x00" + m.Key
		if _, dup := tByKey[fk]; dup {
			issues = append(issues, Issue{Namespace: m.Namespace, Key: m.Key,
				Code: "duplicate-key", Detail: "key appears more than once"})
			continue
		}
		tByKey[fk] = m
	}

	// Structural conflicts (leaf vs parent) surface via Nest.
	if _, err := Nest(target); err != nil {
		issues = append(issues, Issue{Code: "structure", Detail: err.Error()})
	}

	defArgs := make(map[string][]string, len(def))
	for _, m := range def {
		fk := m.Namespace + "\x00" + m.Key
		if parsed, err := ParseICU(m.Value); err == nil {
			defArgs[fk] = parsed.ArgNames()
		}
		if _, ok := tByKey[fk]; !ok {
			issues = append(issues, Issue{Namespace: m.Namespace, Key: m.Key,
				Code: "missing-key", Detail: "key exists in the default locale but not here"})
		}
	}

	for fk, m := range tByKey {
		if err := ValidKeyParts(m.Namespace, m.Key); err != nil {
			issues = append(issues, Issue{Namespace: m.Namespace, Key: m.Key,
				Code: "invalid-key", Detail: err.Error()})
			continue
		}
		issues = append(issues, ValidateValue(m.Namespace, m.Key, m.Value)...)
		parsed, err := ParseICU(m.Value)
		if err != nil {
			continue // icu-syntax already recorded by ValidateValue
		}
		want, isShared := defArgs[fk]
		if !isShared {
			continue // extra key beyond the default locale — allowed
		}
		wantSet := map[string]bool{}
		for _, a := range want {
			wantSet[a] = true
		}
		for _, a := range parsed.ArgNames() {
			if !wantSet[a] {
				issues = append(issues, Issue{Namespace: m.Namespace, Key: m.Key,
					Code: "icu-arg-mismatch",
					Detail: fmt.Sprintf("argument {%s} does not exist in the default locale message (allowed: %s)",
						a, strings.Join(want, ", "))})
			}
		}
	}
	return issues
}
