package i18n

import (
	"fmt"
	"strings"
	"unicode"
)

// ─── Structured ICU MessageFormat parser ─────────────────────────────────────
//
// A small recursive-descent parser for the ICU MessageFormat subset next-intl
// consumes: literal text, '' escaping and 'quoted {literal}' spans, {arg},
// {arg, number|date|time[, style]}, {arg, plural|selectordinal, [offset:n]
// branches…}, {arg, select, branches…} and # inside plural branches. It is a
// real parser (position-tracked tokenizer + AST), not a regex screen — the
// validation layer relies on the extracted argument set and branch structure.

// ICUArg describes one argument referenced by a message.
type ICUArg struct {
	Name string
	Kind string // "value" | "number" | "date" | "time" | "plural" | "selectordinal" | "select"
}

// ICUMessage is the parse result for one message string.
type ICUMessage struct {
	Args []ICUArg
}

// ArgNames returns the sorted unique argument names.
func (m ICUMessage) ArgNames() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(m.Args))
	for _, a := range m.Args {
		if !seen[a.Name] {
			seen[a.Name] = true
			out = append(out, a.Name)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

type icuParser struct {
	src  []rune
	pos  int
	args []ICUArg
}

// ParseICU parses one ICU message and returns its argument structure, or a
// position-annotated error for malformed syntax.
func ParseICU(message string) (ICUMessage, error) {
	p := &icuParser{src: []rune(message)}
	if err := p.parseMessage(0); err != nil {
		return ICUMessage{}, err
	}
	return ICUMessage{Args: p.args}, nil
}

func (p *icuParser) errf(format string, args ...any) error {
	return fmt.Errorf("icu: %s (at offset %d)", fmt.Sprintf(format, args...), p.pos)
}

// parseMessage consumes message text until EOF (depth 0) or an unescaped '}'
// (inside a branch). depth guards runaway nesting.
func (p *icuParser) parseMessage(depth int) error {
	if depth > 20 {
		return p.errf("nesting too deep")
	}
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '\'':
			if err := p.consumeQuoted(); err != nil {
				return err
			}
		case '{':
			p.pos++
			if err := p.parseArgument(depth); err != nil {
				return err
			}
		case '}':
			if depth == 0 {
				return p.errf("unmatched '}'")
			}
			return nil // caller consumes it
		case '#':
			p.pos++ // valid inside plural branches; harmless elsewhere for next-intl
		default:
			p.pos++
		}
	}
	if depth > 0 {
		return p.errf("unterminated branch: missing '}'")
	}
	return nil
}

// consumeQuoted handles ICU apostrophe rules: ” is a literal apostrophe;
// '…' quotes literal text (which may contain { } #) until the closing quote.
func (p *icuParser) consumeQuoted() error {
	p.pos++ // opening '
	if p.pos < len(p.src) && p.src[p.pos] == '\'' {
		p.pos++ // '' → literal apostrophe
		return nil
	}
	// Only starts a quoted span when the next char is syntax-significant;
	// otherwise ICU treats the apostrophe as literal text.
	if p.pos >= len(p.src) || (p.src[p.pos] != '{' && p.src[p.pos] != '}' && p.src[p.pos] != '#') {
		return nil
	}
	for p.pos < len(p.src) {
		if p.src[p.pos] == '\'' {
			p.pos++
			if p.pos < len(p.src) && p.src[p.pos] == '\'' {
				p.pos++ // escaped apostrophe inside quoted span
				continue
			}
			return nil
		}
		p.pos++
	}
	return p.errf("unterminated quoted literal")
}

func (p *icuParser) skipSpace() {
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
}

func (p *icuParser) readIdent() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == ',' || c == '}' || c == '{' || unicode.IsSpace(c) {
			break
		}
		p.pos++
	}
	return string(p.src[start:p.pos])
}

// parseArgument parses the content after '{' up to and including '}'.
func (p *icuParser) parseArgument(depth int) error {
	p.skipSpace()
	name := p.readIdent()
	if name == "" {
		return p.errf("empty argument name")
	}
	p.skipSpace()
	if p.pos >= len(p.src) {
		return p.errf("unterminated argument %q", name)
	}
	if p.src[p.pos] == '}' {
		p.pos++
		p.args = append(p.args, ICUArg{Name: name, Kind: "value"})
		return nil
	}
	if p.src[p.pos] != ',' {
		return p.errf("expected ',' or '}' after argument %q", name)
	}
	p.pos++
	p.skipSpace()
	kind := strings.ToLower(p.readIdent())
	p.skipSpace()
	switch kind {
	case "number", "date", "time":
		p.args = append(p.args, ICUArg{Name: name, Kind: kind})
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++ // optional style token (skeleton/keyword) — consume until '}'
			for p.pos < len(p.src) && p.src[p.pos] != '}' {
				p.pos++
			}
		}
		if p.pos >= len(p.src) || p.src[p.pos] != '}' {
			return p.errf("unterminated %s argument %q", kind, name)
		}
		p.pos++
		return nil
	case "plural", "selectordinal", "select":
		p.args = append(p.args, ICUArg{Name: name, Kind: kind})
		if p.pos >= len(p.src) || p.src[p.pos] != ',' {
			return p.errf("%s argument %q needs branches", kind, name)
		}
		p.pos++
		return p.parseBranches(name, kind, depth)
	default:
		return p.errf("unknown argument type %q for %q", kind, name)
	}
}

// parseBranches parses `key {message} key {message} …}` and enforces the
// mandatory `other` branch for plural/select forms.
func (p *icuParser) parseBranches(argName, kind string, depth int) error {
	sawOther := false
	sawAny := false
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return p.errf("unterminated %s %q", kind, argName)
		}
		if p.src[p.pos] == '}' {
			p.pos++
			if !sawAny {
				return p.errf("%s %q has no branches", kind, argName)
			}
			if !sawOther {
				return p.errf("%s %q is missing the mandatory 'other' branch", kind, argName)
			}
			return nil
		}
		selector := p.readSelector()
		if selector == "" {
			return p.errf("%s %q: expected a branch selector", kind, argName)
		}
		if selector == "offset:" || strings.HasPrefix(selector, "offset:") {
			// plural offset — value may follow with or without space
			p.skipSpace()
			if strings.HasSuffix(selector, ":") {
				p.readIdent()
				p.skipSpace()
			}
			continue
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != '{' {
			return p.errf("%s %q: branch %q must be followed by '{'", kind, argName, selector)
		}
		p.pos++
		if err := p.parseMessage(depth + 1); err != nil {
			return err
		}
		if p.pos >= len(p.src) || p.src[p.pos] != '}' {
			return p.errf("%s %q: branch %q is unterminated", kind, argName, selector)
		}
		p.pos++
		sawAny = true
		if selector == "other" {
			sawOther = true
		}
	}
}

// readSelector reads a branch selector (word, =N or offset:).
func (p *icuParser) readSelector() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '{' || c == '}' || unicode.IsSpace(c) {
			break
		}
		p.pos++
	}
	return string(p.src[start:p.pos])
}
